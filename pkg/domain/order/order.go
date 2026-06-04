package order

import (
	"fmt"
	"strings"
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
	return o.internalState == STATE_PREPARING || o.internalState == STATE_PENDING
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



// OrderRequest は新規発注時にのみ使用する、取引所固有・または一時的なパラメータです
type OrderRequest struct {
	Exchange           ExchangeMarket
	SecurityType       SecurityType
	MarginTradeType    MarginTradeType
	AccountType        AccountType
	ClosePositionOrder ClosePositionOrder
	ClosePositions     []ClosePosition
}

// Order は注文全体を管理する集約ルート（エンティティ）です
type Order struct {
	ID                 string
	Symbol             string
	Action             Action
	Type               OrderType // 🌟 注文種別 (指値・成行)
	OrderPrice         float64   // 発注時の指値（成行の場合は0など）
	OrderQty           float64   // 発注した総数量
	CashMargin         CashMarginType

	CreatedAt  time.Time   // 🌟 発注（オブジェクト作成）時刻
	Executions []Execution // 🌟 約定のコレクション

	status OrderStatus // 注文の状態
	CumQty float64     // 🌟 APIが報告してきた累計約定数量

	IfDone         *Order          // 🌟 この注文が約定した後に有効になる注文

	CancelSentAt time.Time // 🌟 キャンセル送信時刻（ゾンビ防止用）

	Reason string // 🌟 戦略がこの注文を出した理由（子戦略名など）

	// 内部ステータスと疑似約定のトラッキング
	internalState InternalState
	Synthetic     SyntheticFillState

	// Request は新規発注時のリクエストパラメータです
	Request *OrderRequest
}

// SyntheticFillState は疑似約定（Synthetic Fill）の追跡状態を保持します
type SyntheticFillState struct {
	ExpectedAt       time.Time // 疑似約定と判定した時刻
	TouchTimeout     bool      // 疑似約定がタイムアウト（空振り）したかどうかのフラグ
	InitialQueueQty  float64   // タッチした瞬間の板の厚み（自分の前の待ち行列）
	ConsumedVolume   float64   // タッチ後にその価格で消化された累計出来高
	LastVolumeUpdate float64   // 前回のTick時の総出来高
}

type OrderOption func(*Order)

func WithType(t OrderType) OrderOption {
	return func(o *Order) {
		o.Type = t
	}
}

func WithCashMargin(cm CashMarginType) OrderOption {
	return func(o *Order) {
		o.CashMargin = cm
	}
}

func WithRequest(req *OrderRequest) OrderOption {
	return func(o *Order) {
		o.Request = req
	}
}

func WithReason(reason string) OrderOption {
	return func(o *Order) {
		o.Reason = reason
	}
}

func NewOrder(id string, symbol string, action Action, price float64, qty float64, opts ...OrderOption) *Order {
	ord := &Order{
		ID:                 id,
		Symbol:             symbol,
		Action:             action,
		Type:               ORDER_TYPE_LIMIT, // デフォルトは指値
		OrderPrice:         price,
		OrderQty:           qty,
		status:             ORDER_STATUS_WAITING,
		CashMargin:         CASH_MARGIN_MARGIN_ENTRY, // デフォルトは信用新規
		internalState:      STATE_PREPARING,
		CreatedAt:          time.Now(),
	}
	for _, opt := range opts {
		opt(ord)
	}
	return ord
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
	return o.status == ORDER_STATUS_FILLED || o.status == ORDER_STATUS_CANCELED || o.status == ORDER_STATUS_EXPIRED
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

// SendOrderInput は新規発注に必要なパラメータを一括してまとめた構造体です
type SendOrderInput struct {
	Order *Order
}

// GetCancelTimeout は注文の理由や重要度に応じて最適なキャンセルタイムアウト値を返します
func (o *Order) GetCancelTimeout() time.Duration {
	// 緊急決済（ForceExitやPairExitなど）の場合は非常に短い2秒タイムアウト
	if strings.Contains(o.Reason, "Exit") || strings.Contains(o.Reason, "Force") {
		return 2 * time.Second
	}
	// 通常の指値・ブレイクアウト注文キャンセルなどは標準の10秒タイムアウト
	return 10 * time.Second
}

func (o *Order) Status() OrderStatus {
	return o.status
}

func (o *Order) InternalState() InternalState {
	return o.internalState
}

// IsWaiting は注文が待機中かどうかを返します
func (o *Order) IsWaiting() bool {
	return o.status == ORDER_STATUS_WAITING
}

// IsInProgress は注文が執行中かどうかを返します
func (o *Order) IsInProgress() bool {
	return o.status == ORDER_STATUS_IN_PROGRESS
}

// IsFilled は注文が完全約定済かどうかを返します
func (o *Order) IsFilled() bool {
	return o.status == ORDER_STATUS_FILLED
}

// IsCanceled は注文が取消済かどうかを返します
func (o *Order) IsCanceled() bool {
	return o.status == ORDER_STATUS_CANCELED
}

// IsExpired は注文が期限切れ失効したかどうかを返します
func (o *Order) IsExpired() bool {
	return o.status == ORDER_STATUS_EXPIRED
}

// IsCancelSent はキャンセル要求が送信済かどうかを返します
func (o *Order) IsCancelSent() bool {
	return o.status == ORDER_STATUS_CANCEL_SENT
}

// IsFillExpected は疑似約定状態かどうかを返します
func (o *Order) IsFillExpected() bool {
	return o.status == ORDER_STATUS_FILL_EXPECTED
}

// BypassTransition はテストや初期モック設定のために、状態遷移チェックをバイパスして状態を強制セットします
func (o *Order) BypassTransition(status OrderStatus, internalState InternalState) {
	o.status = status
	o.internalState = internalState
}

func (o *Order) ensureNotTerminal() {
	if o.status == ORDER_STATUS_FILLED || o.status == ORDER_STATUS_CANCELED || o.status == ORDER_STATUS_EXPIRED {
		panic(fmt.Sprintf("🚨 [FATAL_STATE_TRANSITION] Cannot transition out of terminal state: %v (OrderID: %s)", o.status, o.ID))
	}
}

func (o *Order) panicInvalidTransition(from, to OrderStatus) {
	panic(fmt.Sprintf("🚨 [INVALID_STATE_TRANSITION] Illegal order status change: %v -> %v (OrderID: %s)", from, to, o.ID))
}

func (o *Order) ensureNotClosed() {
	if o.internalState == STATE_CLOSED {
		panic(fmt.Sprintf("🚨 [FATAL_STATE_TRANSITION] Cannot transition out of closed internal state: %v (OrderID: %s)", o.internalState, o.ID))
	}
}

func (o *Order) panicInvalidInternalTransition(from, to InternalState) {
	panic(fmt.Sprintf("🚨 [INVALID_STATE_TRANSITION] Illegal internal state change: %v -> %v (OrderID: %s)", from, to, o.ID))
}

// ToWaiting は注文ステータスを WAITING に遷移させます
func (o *Order) ToWaiting() {
	from := o.status
	if from == ORDER_STATUS_WAITING {
		return
	}
	o.ensureNotTerminal()

	valid := (from == ORDER_STATUS_NONE || from == ORDER_STATUS_FILL_EXPECTED)
	if !valid {
		o.panicInvalidTransition(from, ORDER_STATUS_WAITING)
	}
	o.status = ORDER_STATUS_WAITING
}

// ToInProgress は注文ステータスを IN_PROGRESS に遷移させます
func (o *Order) ToInProgress() {
	from := o.status
	if from == ORDER_STATUS_IN_PROGRESS {
		return
	}
	o.ensureNotTerminal()

	valid := (from == ORDER_STATUS_NONE || from == ORDER_STATUS_WAITING)
	if !valid {
		o.panicInvalidTransition(from, ORDER_STATUS_IN_PROGRESS)
	}
	o.status = ORDER_STATUS_IN_PROGRESS
}

// ToCancelSent は注文ステータスを CANCEL_SENT に遷移させます
func (o *Order) ToCancelSent() {
	from := o.status
	if from == ORDER_STATUS_CANCEL_SENT {
		return
	}
	o.ensureNotTerminal()

	valid := (from == ORDER_STATUS_NONE || from == ORDER_STATUS_WAITING || from == ORDER_STATUS_IN_PROGRESS || from == ORDER_STATUS_FILL_EXPECTED)
	if !valid {
		o.panicInvalidTransition(from, ORDER_STATUS_CANCEL_SENT)
	}
	o.status = ORDER_STATUS_CANCEL_SENT
}

// ToFillExpected は注文ステータスを FILL_EXPECTED に遷移させます
func (o *Order) ToFillExpected() {
	from := o.status
	if from == ORDER_STATUS_FILL_EXPECTED {
		return
	}
	o.ensureNotTerminal()

	valid := (from == ORDER_STATUS_NONE || from == ORDER_STATUS_WAITING || from == ORDER_STATUS_IN_PROGRESS)
	if !valid {
		o.panicInvalidTransition(from, ORDER_STATUS_FILL_EXPECTED)
	}
	o.status = ORDER_STATUS_FILL_EXPECTED
}

// ToFilled は注文ステータスを FILLED に遷移させます
func (o *Order) ToFilled() {
	from := o.status
	if from == ORDER_STATUS_FILLED {
		return
	}
	o.ensureNotTerminal()
	o.status = ORDER_STATUS_FILLED
}

// ToCanceled は注文ステータスを CANCELED に遷移させます
func (o *Order) ToCanceled() {
	from := o.status
	if from == ORDER_STATUS_CANCELED {
		return
	}
	o.ensureNotTerminal()
	o.status = ORDER_STATUS_CANCELED
}

// ToExpired は注文ステータスを EXPIRED に遷移させます
func (o *Order) ToExpired() {
	from := o.status
	if from == ORDER_STATUS_EXPIRED {
		return
	}
	o.ensureNotTerminal()
	o.status = ORDER_STATUS_EXPIRED
}

// ToPending は内部状態を PENDING に遷移させます
func (o *Order) ToPending() {
	from := o.internalState
	if from == STATE_PENDING {
		return
	}
	o.ensureNotClosed()

	valid := (from == STATE_PREPARING)
	if !valid {
		o.panicInvalidInternalTransition(from, STATE_PENDING)
	}
	o.internalState = STATE_PENDING
}

// ToActive は内部状態を ACTIVE に遷移させます
func (o *Order) ToActive() {
	from := o.internalState
	if from == STATE_ACTIVE {
		return
	}
	o.ensureNotClosed()

	valid := (from == STATE_PREPARING || from == STATE_PENDING)
	if !valid {
		o.panicInvalidInternalTransition(from, STATE_ACTIVE)
	}
	o.internalState = STATE_ACTIVE
}

// ToCanceling は内部状態を CANCELING に遷移させます
func (o *Order) ToCanceling() {
	from := o.internalState
	if from == STATE_CANCELING {
		return
	}
	o.ensureNotClosed()

	valid := (from == STATE_ACTIVE)
	if !valid {
		o.panicInvalidInternalTransition(from, STATE_CANCELING)
	}
	o.internalState = STATE_CANCELING
}

// ToClosed は内部状態を CLOSED に遷移させます
func (o *Order) ToClosed() {
	from := o.internalState
	if from == STATE_CLOSED {
		return
	}
	o.ensureNotClosed()
	o.internalState = STATE_CLOSED
}

// TransitionToStatus は指定されたステータスへの遷移を実行します（動的な状態同期用）
func (o *Order) TransitionToStatus(to OrderStatus) {
	switch to {
	case ORDER_STATUS_WAITING:
		o.ToWaiting()
	case ORDER_STATUS_IN_PROGRESS:
		o.ToInProgress()
	case ORDER_STATUS_FILLED:
		o.ToFilled()
	case ORDER_STATUS_CANCELED:
		o.ToCanceled()
	case ORDER_STATUS_EXPIRED:
		o.ToExpired()
	case ORDER_STATUS_CANCEL_SENT:
		o.ToCancelSent()
	case ORDER_STATUS_FILL_EXPECTED:
		o.ToFillExpected()
	}
}

// TransitionToInternalState は指定された内部状態への遷移を実行します（動的な状態同期用）
func (o *Order) TransitionToInternalState(to InternalState) {
	switch to {
	case STATE_PENDING:
		o.ToPending()
	case STATE_ACTIVE:
		o.ToActive()
	case STATE_CANCELING:
		o.ToCanceling()
	case STATE_CLOSED:
		o.ToClosed()
	}
}

