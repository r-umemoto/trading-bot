// internal/usecase/trade_usecase.go
package usecase

import (
	"context"
	"fmt"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
)

// TradeUseCase は価格更新イベントを受け取り、該当するスナイパーに伝達するユースケースです
type TradeUseCase struct {
	snipers  []*sniper.Sniper
	gateway  market.MarketGateway
	analyzer market.Analyzer
}

func NewTradeUseCase(snipers []*sniper.Sniper, gateway market.MarketGateway, analyzer market.Analyzer) *TradeUseCase {
	return &TradeUseCase{
		snipers:  snipers,
		gateway:  gateway,
		analyzer: analyzer,
	}
}

// HandleTick は市場のTickデータを受け取り、担当スナイパーの思考と執行をトリガーします
func (u *TradeUseCase) HandleTick(ctx context.Context, tick market.Tick) {
	u.analyzer.UpdateTick(tick)
	state := u.analyzer.GetState(tick.Symbol)

	for _, s := range u.snipers {
		if s.Symbol == tick.Symbol {
			// 1. スナイパーに考えさせる（純粋な関数）
			req := s.Tick(state)

			if req != nil {
				// 2. 要求があれば、市場（インフラ）に発注する
				orderID, err := u.gateway.SendOrder(ctx, *req)
				if err != nil {
					fmt.Printf("❌ 発注失敗: %v\n", err)
					continue
				}
				order := market.NewOrder(orderID, req.Symbol, req.Action, req.Price, req.Qty)

				// 3. 発注が成功したら、スナイパーにIDを覚えさせる
				s.RecordOrder(order)
				fmt.Printf("✅ 注文受付IDを記録しました: %s\n", orderID)
			}
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
