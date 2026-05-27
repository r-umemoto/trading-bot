package market

import (
	"context"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/position"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

type ResisterSymbolRequest struct {
	Symbol   string
	Exchange order.ExchangeMarket
}

type MarketChannels struct {
	Ticks  map[string]<-chan tick.Tick
	Orders map[string]<-chan order.Orders
}

// SymbolChannels は特定の銘柄に紐づくイベントチャネルのセットです
type SymbolChannels struct {
	Tick  <-chan tick.Tick
	Order <-chan order.Orders
}

// MarketGateway は市場への統合アクセスポイントです
type MarketGateway interface {
	// Listen は市場との接続（WebSocket/Polling）を開始し、イベントチャネル群を返します
	Listen(ctx context.Context) (*MarketChannels, error)

	// DataPool はゲートウェイが内部で保持・更新する時価データプールを取得します
	DataPool() tick.DataPool

	SendOrder(ctx context.Context, input order.SendOrderInput) (*order.Order, error)
	CancelOrder(ctx context.Context, orderID string) error
	GetPositions(ctx context.Context, product order.ProductType) ([]position.Position, error)
	GetOrders(ctx context.Context) (order.Orders, error)
	GetSymbol(ctx context.Context, symbol string, exchange order.ExchangeMarket) (symbol.Symbol, error)
	RegisterSymbol(ctx context.Context, req ResisterSymbolRequest) error
	RegisterSymbols(ctx context.Context, reqs []ResisterSymbolRequest) error
	UnregisterSymbolAll(ctx context.Context) error
}
