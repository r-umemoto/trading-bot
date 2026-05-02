package market

import (
	"context"
)

// OrdersReport は最新の注文状態の一覧を通知します
type OrdersReport struct {
	Orders []Order
}

type OrderRequest struct {
	Symbol             string
	Exchange           ExchangeMarket
	SecurityType       SecurityType
	Action             Action
	MarginTradeType    MarginTradeType
	AccountType        AccountType
	ClosePositionOrder ClosePositionOrder
	ClosePositions     []ClosePosition // 指定返済用
	OrderType          OrderType
	Qty                float64
	Price              float64
}

type ResisterSymbolRequest struct {
	Symbol   string
	Exchange ExchangeMarket
}

// MarketGateway は市場への統合アクセスポイントです
type MarketGateway interface {
	// Start は市場との接続を開始し、リアルタイム情報の受信を開始します
	Start(ctx context.Context) (<-chan Tick, <-chan OrdersReport, error)

	SendOrder(ctx context.Context, req OrderRequest) (string, error) // 戻り値は受付OrderID
	CancelOrder(ctx context.Context, orderID string) error
	GetPositions(ctx context.Context, product ProductType) ([]Position, error)
	GetOrders(ctx context.Context) ([]Order, error)
	GetSymbol(ctx context.Context, symbol string, exchange ExchangeMarket) (Symbol, error)
	RegisterSymbol(ctx context.Context, req ResisterSymbolRequest) error
	UnregisterSymbolAll(ctx context.Context) error
}
