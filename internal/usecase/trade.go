// internal/usecase/trade_usecase.go
package usecase

import (
	"trading-bot/internal/domain/market"
	"trading-bot/internal/domain/sniper"
)

// TradeUseCase は価格更新イベントを受け取り、該当するスナイパーに伝達するユースケースです
type TradeUseCase struct {
	snipers []*sniper.Sniper
}

func NewTradeUseCase(snipers []*sniper.Sniper) *TradeUseCase {
	return &TradeUseCase{snipers: snipers}
}

// HandleTick は市場のTickデータを受け取り、担当スナイパーの思考と執行をトリガーします
func (u *TradeUseCase) HandleTick(tick market.Tick) {
	for _, s := range u.snipers {
		if s.Symbol == tick.Symbol {
			s.Tick(tick.Price)
		}
	}
}

// HandleExecution は、インフラ層から流れてきた約定通知を該当するスナイパーにルーティングします
func (u *TradeUseCase) HandleExecution(report market.ExecutionReport) {
	for _, s := range u.snipers {
		if s.Symbol == report.Symbol {
			s.OnExecution(report)
			return // 該当銘柄は1つと想定し、見つけたら終了
		}
	}
}
