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
	snipers      []*sniper.Sniper
	gateway      market.MarketGateway
	analyzer     market.Analyzer
	tickChannels map[string]chan market.Tick // 銘柄ごとのTick処理チャネル
}

func NewTradeUseCase(snipers []*sniper.Sniper, gateway market.MarketGateway, analyzer market.Analyzer) *TradeUseCase {
	uc := &TradeUseCase{
		snipers:      snipers,
		gateway:      gateway,
		analyzer:     analyzer,
		tickChannels: make(map[string]chan market.Tick),
	}

	// 銘柄ごとにチャネルを作成
	for _, s := range snipers {
		if _, exists := uc.tickChannels[s.Symbol]; !exists {
			// バッファサイズは適宜調整（ここでは100）
			uc.tickChannels[s.Symbol] = make(chan market.Tick, 100)
		}
	}

	return uc
}

// StartWorkers は銘柄ごとのワーカー（Goroutine）を起動します
// Engineの起動時（Run）などに呼ばれることを想定しています
func (u *TradeUseCase) StartWorkers(ctx context.Context) {
	for symbol, ch := range u.tickChannels {
		go u.worker(ctx, symbol, ch)
	}
}

// worker は特定の銘柄のTickを専用に処理するGoroutineです
func (u *TradeUseCase) worker(ctx context.Context, symbol string, ch <-chan market.Tick) {
	for {
		select {
		case <-ctx.Done():
			return
		case tick := <-ch:
			// この銘柄を担当するスナイパーを探して処理を実行
			u.processTickForSymbol(ctx, tick, symbol)
		}
	}
}

func (u *TradeUseCase) processTickForSymbol(ctx context.Context, tick market.Tick, symbol string) {
	u.analyzer.UpdateTick(tick)
	state := u.analyzer.GetState(symbol)

	for _, s := range u.snipers {
		if s.Symbol == symbol {
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

// HandleTick は市場のTickデータを受け取り、該当銘柄のチャネルへルーティングします
func (u *TradeUseCase) HandleTick(ctx context.Context, tick market.Tick) {
	if ch, ok := u.tickChannels[tick.Symbol]; ok {
		select {
		case ch <- tick:
			// 正常にキューイング完了
		default:
			// チャネルが詰まっている場合（ワーカの処理が追いついていない）
			fmt.Printf("⚠️ 警告: %s のTickチャネルがフルです。Tickがスキップされるか遅延します。\n", tick.Symbol)
			// ブロックさせるか、破棄するかは要件次第（ここではブロックする）
			ch <- tick
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
