// internal/usecase/trade_usecase.go
package usecase

import (
	"context"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/service"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
)

// TradeUseCase は価格更新イベントを受け取り、該当するスナイパーに伝達するユースケースです
type TradeUseCase struct {
	nests      []*SniperNest
	gateway    market.MarketGateway
}

func NewTradeUseCase(nests []*SniperNest, gateway market.MarketGateway) *TradeUseCase {
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
		nest.Start(ctx, symChs)
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

