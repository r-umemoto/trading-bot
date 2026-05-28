// internal/usecase/trade_usecase.go
package usecase

import (
	"context"
	"fmt"
	"time"

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
				go u.fire(ctx, nest.SymbolCode, act.Sniper, act.Bullet)
			}
		case ords := <-chs.Order:
			nest.Spotter.Update(ords, time.Now())
		}
	}
}

// fire は実際の発注・キャンセル処理を API ゲートウェイに対して非同期に実行します
func (u *TradeUseCase) fire(ctx context.Context, symbol string, s *sniper.Sniper, b sniper.Bullet) {
	if b.HasCancel() {
		err := u.gateway.CancelOrder(ctx, b.CancelOrderID)
		if err != nil {
			fmt.Printf("キャンセル失敗 (ID: %s): %v\n", b.CancelOrderID, err)
		}
	}

	if b.HasOrder() {
		updatedOrder, err := u.gateway.SendOrder(ctx, order.SendOrderInput{Order: b.Order, Request: *b.Request})
		if err != nil {
			fmt.Printf("発注失敗 (Symbol: %s): %v\n", symbol, err)
			s.FailSendingOrder(b.Order)
			return
		}
		s.UpdateOrderID(b.Order, updatedOrder.ID)
	}
}

func (u *TradeUseCase) PrintPerformanceReport(enableCSV bool) {
	var targets []service.ReportableTarget
	for _, n := range u.nests {
		for _, s := range n.Snipers {
			targets = append(targets, s)
		}
	}
	report := service.GeneratePerformanceReport(u, targets, u.gateway.DataPool())
	presenter := NewReportPresenter()
	presenter.PrintPerformanceReport(report, enableCSV)
}

func (u *TradeUseCase) GetPerformance(sniperID string) sniper.Performance {
	for _, nest := range u.nests {
		for _, s := range nest.Snipers {
			if s.ID == sniperID {
				return nest.Spotter.GetPerformance(sniperID)
			}
		}
	}
	return sniper.Performance{}
}

func (u *TradeUseCase) GetUnrealizedPnL(sniperID string, currentPrice float64) float64 {
	for _, nest := range u.nests {
		for _, s := range nest.Snipers {
			if s.ID == sniperID {
				return nest.Spotter.GetUnrealizedPnL(sniperID, currentPrice)
			}
		}
	}
	return 0
}
