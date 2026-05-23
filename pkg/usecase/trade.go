// internal/usecase/trade_usecase.go
package usecase

import (
	"context"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/service"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

// TradeUseCase は価格更新イベントを受け取り、該当するスナイパーに伝達するユースケースです
type TradeUseCase struct {
	snipers    []*sniper.Sniper
	nests      []*SniperNest // 🌟 銘柄（ターゲット）ごとの狙撃陣地（SniperNest）のリスト
	gateway    market.MarketGateway
	dispatcher *service.OrderDispatcher
	reporter   *service.PerformanceReporter
}

func NewTradeUseCase(snipers []*sniper.Sniper, gateway market.MarketGateway) *TradeUseCase {
	return &TradeUseCase{
		snipers:    snipers,
		gateway:    gateway,
		dispatcher: service.NewOrderDispatcher(gateway),
		reporter:   service.NewPerformanceReporter(snipers, gateway.DataPool()),
	}
}

// Start は発注ディスパッチャと市場データ受信ワーカーを起動します
func (u *TradeUseCase) Start(ctx context.Context, ticks map[string]<-chan tick.Tick, orders map[string]<-chan order.Orders) {
	u.dispatcher.Start(ctx)

	u.nests = make([]*SniperNest, 0, len(ticks))

	// 各銘柄専用の SniperNest を起動
	for symbol, tickCh := range ticks {
		orderCh := orders[symbol]

		// この銘柄に紐づくスナイパーをフィルター
		var symbolSnipers []*sniper.Sniper
		for _, s := range u.snipers {
			if s.Detail.Code == symbol {
				symbolSnipers = append(symbolSnipers, s)
			}
		}

		// 🌟 スナイパーが1つ以上ある場合、共有の Spotter を作成
		var spotter *sniper.Spotter
		if len(symbolSnipers) > 0 {
			spotter = sniper.NewSpotter(symbolSnipers[0].Detail, symbolSnipers[0].Logger)
		}

		nest := NewSniperNest(symbol, spotter, symbolSnipers, tickCh, orderCh, u.dispatcher)
		nest.Start(ctx)
		u.nests = append(u.nests, nest)
	}
}


// PrintPerformanceReport summarizes and prints the performance of all snipers.
func (u *TradeUseCase) PrintPerformanceReport(enableCSV bool) {
	u.reporter.PrintPerformanceReport(enableCSV)
}
