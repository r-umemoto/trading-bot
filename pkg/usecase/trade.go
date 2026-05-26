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
	snipers    []*sniper.Sniper
	nests      []*SniperNest
	gateway    market.MarketGateway
	reporter   *service.PerformanceReporter
}

func NewTradeUseCase(snipers []*sniper.Sniper, gateway market.MarketGateway) *TradeUseCase {
	u := &TradeUseCase{
		snipers: snipers,
		gateway: gateway,
	}
	u.reporter = service.NewPerformanceReporter(u, snipers, gateway.DataPool())
	return u
}

// Start は市場データ受信を開始し、各銘柄ごとの SniperNest を起動します
func (u *TradeUseCase) Start(ctx context.Context, chs *market.MarketChannels) {
	u.nests = make([]*SniperNest, 0, len(chs.Ticks))

	for symbol, tickCh := range chs.Ticks {
		symChs := market.SymbolChannels{
			Tick:  tickCh,
			Order: chs.Orders[symbol],
		}

		var symbolSnipers []*sniper.Sniper
		for _, s := range u.snipers {
			if s.Detail.Code == symbol {
				symbolSnipers = append(symbolSnipers, s)
			}
		}

		var spotter *sniper.Spotter
		if len(symbolSnipers) > 0 {
			spotter = sniper.NewSpotter(symbolSnipers[0].Detail, symbolSnipers[0].Logger)
		}

		nest := NewSniperNest(symbol, spotter, symbolSnipers, symChs, u.gateway)
		nest.Start(ctx)
		u.nests = append(u.nests, nest)
	}
}

func (u *TradeUseCase) PrintPerformanceReport(enableCSV bool) {
	u.reporter.PrintPerformanceReport(enableCSV)
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
