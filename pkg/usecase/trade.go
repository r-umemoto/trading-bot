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

		nest := NewSniperNest(symbol, symbolSnipers, tickCh, orderCh, u.dispatcher)
		nest.Start(ctx)
		u.nests = append(u.nests, nest)
	}
}

// ExecuteTick は指定された銘柄の価格更新（Tick）を受け取り、同期的にスナイパー戦略を処理・評価します
func (u *TradeUseCase) ExecuteTick(ctx context.Context, t tick.Tick) {
	for _, s := range u.snipers {
		if s.Detail.Code == t.Symbol {
			// 1. スナイパーに考えさせる
			bullet := s.Tick()

			// 2. 🌟 直接発注せず、ディスパッチャに委ねる
			u.dispatcher.Submit(s, bullet)
		}
	}
}

// ExecuteExecutionReport は最新の注文レポートを受け取り、同期的にスナイパーと注文状態の同期を行います
func (u *TradeUseCase) ExecuteExecutionReport(ctx context.Context, report order.Orders, symbol string) {
	for _, s := range u.snipers {
		if s.Detail.Code == symbol {
			bullet := s.SyncOrders(report)

			// 2. 🌟 直接発注せず、ディスパッチャに委ねる
			u.dispatcher.Submit(s, bullet)
		}
	}
}

// PrintPerformanceReport summarizes and prints the performance of all snipers.
func (u *TradeUseCase) PrintPerformanceReport(enableCSV bool) {
	u.reporter.PrintPerformanceReport(enableCSV)
}
