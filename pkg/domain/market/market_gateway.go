package market

import (
	"context"

	"github.com/r-umemoto/trading-bot/pkg/domain/ord"
	"github.com/r-umemoto/trading-bot/pkg/domain/position"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

type ResisterSymbolRequest struct {
	Symbol   string
	Exchange ord.ExchangeMarket
}

// MarketGateway は市場への統合アクセスポイントです
type MarketGateway interface {
	// Start は市場との接続を開始し、リアルタイム情報の受信を開始します
	Start(ctx context.Context) (<-chan tick.Tick, <-chan ord.Orders, error)

	SendOrder(ctx context.Context, order ord.Order) (ord.Order, error) // 引数で渡されたOrderにIDとStatusを書き込んだ新しいOrderを返す
	CancelOrder(ctx context.Context, orderID string) error
	GetPositions(ctx context.Context, product ord.ProductType) ([]position.Position, error)
	GetOrders(ctx context.Context) (ord.Orders, error)
	GetSymbol(ctx context.Context, symbol string, exchange ord.ExchangeMarket) (symbol.Symbol, error)
	RegisterSymbol(ctx context.Context, req ResisterSymbolRequest) error
	RegisterSymbols(ctx context.Context, reqs []ResisterSymbolRequest) error
	UnregisterSymbolAll(ctx context.Context) error
}
