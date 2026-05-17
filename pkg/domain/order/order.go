package order

import (
	"fmt"
	"time"
)

// InternalState は注文のライフサイクルを管理するBot内部の状態です
type InternalState int

const (
	STATE_PREPARING InternalState = iota // 準備中・発注前
	STATE_PENDING                        // APIへ送信済・受付ID待ち
	STATE_ACTIVE                         // APIでID確定・板に並んでいる状態
	STATE_CANCELING                      // キャンセル要求送信済
	STATE_CLOSED                         // 完了（完全約定、またはキャンセル済）
)

// IsPending は注文がまだ取引所に到達していない（API受付前）状態かどうかを返します
func (o *Order) IsPending() bool {
	return o.InternalState == STATE_PREPARING || o.InternalState == STATE_PENDING
}

type OrderStatus uint32

const (
	ORDER_STATUS_NONE          OrderStatus = 0
	ORDER_STATUS_WAITING       OrderStatus = 1 // 発注受付中・取引所送信前
	ORDER_STATUS_IN_PROGRESS   OrderStatus = 2 // 取引所にて執行中（一部約定を含む）
	ORDER_STATUS_FILLED        OrderStatus = 3 // 全約定（完了）
	ORDER_STATUS_CANCELED      OrderStatus = 4 // 取消済（完了）
	ORDER_STATUS_EXPIRED       OrderStatus = 5 // 失効・期限切れ（完了）
	ORDER_STATUS_CANCEL_SENT   OrderStatus = 6 // キャンセル送信済み・確認待ち
	ORDER_STATUS_FILL_EXPECTED OrderStatus = 7 // 疑似約定（貫通確認済・公式通知待ち）
)

// Execution は1回の約定の事実を表す値オブジェクトです
type Execution struct {
	ID            string
	Price         float64
	Qty           float64
	ExecutionTime time.Time // 🌟 約定日時
}

// Order は注文全体を管理する集約ルート（エンティティ）です
type Order struct {
	ID         string
	Symbol     string
	Action     Action
	OrderPrice float64 // 発注時の指値（成行の場合は0など）
	OrderQty   float64 // 発注した総数量

	CreatedAt  time.Time   // 🌟 発注（オブジェクト作成）時刻
	Executions []Execution // 🌟 約定のコレクション

	Status OrderStatus // 注文の状態
	CumQty float64     // 🌟 APIが報告してきた累計約定数量

	HasIFD       bool      // 🌟 IFD注文の有無
	IFDAction    Action    // IFD注文のアクション (BUY/SELL)
	IFDPrice     float64   // IFD注文の価格
	IFDOrderType OrderType // IFD注文の執行条件

	CancelSentAt time.Time // 🌟 キャンセル送信時刻（ゾンビ防止用）

	// 新たに追加する発注パラメータ
	Exchange           ExchangeMarket
	SecurityType       SecurityType
	MarginTradeType    MarginTradeType
	AccountType        AccountType
	ClosePositionOrder ClosePositionOrder
	ClosePositions     []ClosePosition // 指定返済用
	OrderType          OrderType

	// 内部ステータスと疑似約定のトラッキング
	InternalState InternalState
	Synthetic     SyntheticFillState
}

// SyntheticFillState は疑似約定（Synthetic Fill）の追跡状態を保持します
type SyntheticFillState struct {
	ExpectedAt       time.Time // 疑似約定と判定した時刻
	TouchTimeout     bool      // 疑似約定がタイムアウト（空振り）したかどうかのフラグ
	InitialQueueQty  float64   // タッチした瞬間の板の厚み（自分の前の待ち行列）
	ConsumedVolume   float64   // タッチ後にその価格で消化された累計出来高
	LastVolumeUpdate float64   // 前回のTick時の総出来高
}

func NewOrder(id string, symbol string, action Action, price float64, qty float64) Order {
	return Order{
		ID:            id,
		Symbol:        symbol,
		Action:        action,
		OrderPrice:    price,
		OrderQty:      qty,
		Status:        ORDER_STATUS_WAITING,
		InternalState: STATE_PREPARING,
		CreatedAt:     time.Now(),
	}
}

func NewOrderPtr(id string, symbol string, action Action, price float64, qty float64) *Order {
	return &Order{
		ID:            id,
		Symbol:        symbol,
		Action:        action,
		OrderPrice:    price,
		OrderQty:      qty,
		Status:        ORDER_STATUS_WAITING,
		InternalState: STATE_PREPARING,
		CreatedAt:     time.Now(),
	}
}

// FilledQty は現在までに約定した合計数量を返します
func (o *Order) FilledQty() float64 {
	var sum float64
	for _, exec := range o.Executions {
		sum += exec.Qty
	}
	return sum
}

// AveragePrice は約定済みの平均単価を返します（未約定の場合は0）
func (o *Order) AveragePrice() float64 {
	if len(o.Executions) == 0 {
		return 0.0
	}
	var totalCost float64
	var totalQty float64
	for _, exec := range o.Executions {
		totalCost += exec.Price * float64(exec.Qty)
		totalQty += exec.Qty
	}
	if totalQty == 0 {
		return 0.0
	}
	return totalCost / float64(totalQty)
}

// IsCompleted は注文が完全に終了したか（全約定 or キャンセル or 期限切れ）を判定します
func (o *Order) IsCompleted() bool {
	return o.Status == ORDER_STATUS_FILLED || o.Status == ORDER_STATUS_CANCELED || o.Status == ORDER_STATUS_EXPIRED
}

// HasExecution は指定された約定IDが既に存在するかを判定します
func (o *Order) HasExecution(execID string) bool {
	for _, exec := range o.Executions {
		if exec.ID == execID {
			return true
		}
	}
	return false
}

// AddExecution は新しい約定を追加します（重複チェック付き）
func (o *Order) AddExecution(exec Execution) {
	// 既に同じ約定IDが存在すれば無視（冪等性の担保）
	for _, existing := range o.Executions {
		if existing.ID == exec.ID {
			return
		}
	}
	o.Executions = append(o.Executions, exec)
}

const LOCAL_ID_PREFIX = "local-"

// GenerateLocalID はAPIからのレスポンス待ちの間に使用するローカル専用の仮IDを生成します
func GenerateLocalID() string {
	return fmt.Sprintf("%s%d", LOCAL_ID_PREFIX, time.Now().UnixNano())
}

// Orders は最新の注文状態の一覧を通知します
type Orders struct {
	Orders []Order
}
