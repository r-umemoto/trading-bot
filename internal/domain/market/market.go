package market

import (
	"context"
)

// Tick はシステム共通の価格データ（カブコムの仕様を一切知らない純粋なデータ）
type Tick struct {
	Symbol string
	Price  float64
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
	Qty     uint32  // 実際に約定した数量
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
	Qty    int
}

// OrderRequest は市場へ送る注文の要望です
type Position struct {
	Symbol string  // 銘柄
	Qty    uint32  // 数数
	Price  float64 // 取得価格
}

// OrderBroker は市場へ注文を仲介する規格です（インフラ層で実装します）
type OrderBroker interface {
	SendOrder(ctx context.Context, req OrderRequest) (string, error) // 戻り値は受付OrderID
	CancelOrder(ctx context.Context, orderID string) error
	GetOrders(ctx context.Context, product ProductType) ([]Position, error)
}
