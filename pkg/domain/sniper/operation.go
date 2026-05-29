package sniper

import (
	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

// Operation は、特定の取引作戦（単一銘柄トレンドフォロー、ペアトレード等）を表す最上位のドメイン境界（Aggregate Root）です。
type Operation interface {
	GetID() string
	GetSymbolCode() string
	GetSymbolCodes() []string
	GetExchanges() []order.ExchangeMarket

	HandleTick(t tick.Tick) []FireAction
	UpdateOrders(report order.Orders)
	ForceExit()
	GetActiveOrders() []*order.Order
	GetReportableTargets() []ReportableTarget

	HasSniper(sniperID string) bool
	FailSendingOrder(sniperID string, ord *order.Order)
	UpdateOrderID(sniperID string, ord *order.Order, newID string)

	GetPerformance(sniperID string) Performance
	GetUnrealizedPnL(sniperID string, currentPrice float64) float64
}

// DefaultOperation は、1つの SniperNest を包むデフォルト（単一銘柄）の Operation 実装です。
// Goの構造体埋め込み（Struct Embedding）を活用して、メソッドの委譲コードを最小限に抑えています。
type DefaultOperation struct {
	ID string
	*SniperNest
}

func NewDefaultOperation(id string, nest *SniperNest) *DefaultOperation {
	return &DefaultOperation{
		ID:         id,
		SniperNest: nest,
	}
}

// GetID は作戦のIDを返します。
func (o *DefaultOperation) GetID() string {
	return o.ID
}

// GetSymbolCodes は作戦に関与する全銘柄のコードを返します（単一銘柄では1つのみ）。
func (o *DefaultOperation) GetSymbolCodes() []string {
	return []string{o.SymbolCode}
}
