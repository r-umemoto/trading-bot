package market

import (
	"context"
)

// Tick はシステム共通の価格データ（カブコムの仕様を一切知らない純粋なデータ）
type Tick struct {
	Symbol string
	Price  float64
	VWAP   float64
}

type Action string

const (
	Buy  Action = "BUY"
	Sell Action = "SELL"
)

type ProductType int

const (
	ProductCash   ProductType = iota // 現物 (0)
	ProductMargin                    // 信用 (1)
)

// ExecutionReport は市場で発生した約定の事実を表します
type ExecutionReport struct {
	OrderID string  // 紐づく注文ID
	Symbol  string  // 銘柄
	Action  Action  // 買いか売りか
	Price   float64 // 実際の約定単価
	Qty     float64 // 実際に約定した数量
}

// EventStreamer は、市場で発生するあらゆるイベントを受信するための規格です
type EventStreamer interface {
	// Start は市場との接続を開始し、2つのイベントチャネルを返します
	Start(ctx context.Context) (<-chan Tick, <-chan ExecutionReport, error)
}

// OrderRequest は市場へ送る注文の要望です
type OrderRequest struct {
	Symbol string
	Action Action
	Qty    float64
}

// OrderRequest は市場へ送る注文の要望です
type Position struct {
	Symbol string  // 銘柄
	Qty    float64 // 数数
	Price  float64 // 取得価格
}

type OrderState uint32

const (
	WAITING     OrderState = 1
	PROCCESSING OrderState = 2
	PROCCESSED  OrderState = 3
	FINISH      OrderState = 4
)

type Side string

const (
	SideBuy  Side = "1"
	SideSell Side = "2"
)

type Order struct {
	ID       string     `json:"ID"`       // 注文ID（キャンセル時に必要）
	State    OrderState `json:"State"`    // 状態（3: 処理中/待機中, 5: 終了 など）
	Symbol   string     `json:"Symbol"`   // 銘柄コード
	Side     Side       `json:"Side"`     // 売買区分
	OrderQty float64    `json:"OrderQty"` // 発注数量
	CumQty   float64    `json:"CumQty"`   // 約定数量
	Price    float64    `json:"Price"`    // 値段
}

// OrderBroker は市場へ注文を仲介する規格です（インフラ層で実装します）
type OrderBroker interface {
	SendOrder(ctx context.Context, req OrderRequest) (string, error) // 戻り値は受付OrderID
	CancelOrder(ctx context.Context, orderID string) error
	GetPositions(ctx context.Context, product ProductType) ([]Position, error)
	GetOrders(ctx context.Context) ([]Order, error)
}
