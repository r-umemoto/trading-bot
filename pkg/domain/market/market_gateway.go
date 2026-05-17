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

// MarketGateway は市場への統合アクセスポイントです
type MarketGateway interface {
	// Listen は市場との接続（WebSocket/Polling）を開始し、各銘柄専用のチャネルマップを返します
	Listen(ctx context.Context) (map[string]<-chan tick.Tick, map[string]<-chan order.Orders, error)

	// DataPool はゲートウェイが内部で保持・更新する時価データプールを取得します
	DataPool() tick.DataPool

	SendOrder(ctx context.Context, input order.SendOrderInput) (order.Order, error) // 引数で渡されたOrderにIDとStatusを書き込んだ新しいOrderを返す
	CancelOrder(ctx context.Context, orderID string) error
	GetPositions(ctx context.Context, product order.ProductType) ([]position.Position, error)
	GetOrders(ctx context.Context) (order.Orders, error)
	GetSymbol(ctx context.Context, symbol string, exchange order.ExchangeMarket) (symbol.Symbol, error)
	RegisterSymbol(ctx context.Context, req ResisterSymbolRequest) error
	RegisterSymbols(ctx context.Context, reqs []ResisterSymbolRequest) error
	UnregisterSymbolAll(ctx context.Context) error
}
