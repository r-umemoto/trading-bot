// internal/usecase/trade_usecase.go
package usecase

import (
	"context"
	"fmt"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/service"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
)

// TradeUseCase は価格更新イベントを受け取り、該当するスナイパーに伝達するユースケースです
type TradeUseCase struct {
	nests   []*sniper.SniperNest
	gateway market.MarketGateway
}

func NewTradeUseCase(nests []*sniper.SniperNest, gateway market.MarketGateway) *TradeUseCase {
	return &TradeUseCase{
		nests:   nests,
		gateway: gateway,
	}
}

// Start は市場データ受信を開始し、各銘柄ごとの SniperNest を起動します
func (u *TradeUseCase) Start(ctx context.Context, chs *market.MarketChannels) {
	for _, nest := range u.nests {
		tickCh := chs.Ticks[nest.SymbolCode]
		orderCh := chs.Orders[nest.SymbolCode]
		symChs := market.SymbolChannels{
			Tick:  tickCh,
			Order: orderCh,
		}
		go u.runNestEventLoop(ctx, nest, symChs)
	}
}

// runNestEventLoop は特定の銘柄（SniperNest）のイベントループを非同期に監視します
func (u *TradeUseCase) runNestEventLoop(ctx context.Context, nest *sniper.SniperNest, chs market.SymbolChannels) {
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-chs.Tick:
			// ドメイン集約にビジネスロジックの評価を委譲 (純粋関数)
			actions := nest.HandleTick(t)
			for _, act := range actions {
				go u.fire(ctx, nest, act.SniperID, act.Bullet)
			}
		case ords := <-chs.Order:
			nest.UpdateOrders(ords)
		}
	}
}

// fire は実際の発注・キャンセル処理を API ゲートウェイに対して非同期に実行します
func (u *TradeUseCase) fire(ctx context.Context, nest *sniper.SniperNest, sniperID string, b sniper.Bullet) {
	if b.HasCancel() {
		err := u.gateway.CancelOrder(ctx, b.CancelOrderID)
		if err != nil {
			fmt.Printf("キャンセル失敗 (ID: %s): %v\n", b.CancelOrderID, err)
		}
	}

	if b.HasOrder() {
		updatedOrder, err := u.gateway.SendOrder(ctx, order.SendOrderInput{Order: b.Order, Request: *b.Request})
		if err != nil {
			fmt.Printf("発注失敗 (Symbol: %s): %v\n", nest.SymbolCode, err)
			nest.FailSendingOrder(sniperID, b.Order)
			return
		}
		nest.UpdateOrderID(sniperID, b.Order, updatedOrder.ID)
	}
}

func (u *TradeUseCase) PrintPerformanceReport(enableCSV bool) {
	var targets []sniper.ReportableTarget
	for _, n := range u.nests {
		targets = append(targets, n.GetReportableTargets()...)
	}
	report := service.GeneratePerformanceReport(u, targets, u.gateway.DataPool())
	presenter := NewReportPresenter()
	presenter.PrintPerformanceReport(report, enableCSV)
}

func (u *TradeUseCase) GetPerformance(sniperID string) sniper.Performance {
	for _, nest := range u.nests {
		if nest.HasSniper(sniperID) {
			return nest.GetPerformance(sniperID)
		}
	}
	return sniper.Performance{}
}

func (u *TradeUseCase) GetUnrealizedPnL(sniperID string, currentPrice float64) float64 {
	for _, nest := range u.nests {
		if nest.HasSniper(sniperID) {
			return nest.GetUnrealizedPnL(sniperID, currentPrice)
		}
	}
	return 0
}
