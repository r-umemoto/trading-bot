// cmd/bot/portfolio.go
package main

import (
	"fmt"

	"trading-bot/internal/domain/market"
	"trading-bot/internal/domain/sniper"
	"trading-bot/internal/domain/sniper/strategy"
	"trading-bot/internal/infra/kabu"
	"trading-bot/internal/usecase"
)

// buildPortfolio はすべての依存関係を解決し、実行可能なEngineを構築します
func buildPortfolio(client *kabu.KabuClient, apiPassword string) *Engine {
	var snipers []*sniper.Sniper
	var watchSymbols []string

	// 1. 発注アダプターの生成
	var executor sniper.OrderExecutor = kabu.NewKabuExecutor(client, apiPassword)

	// 2. 監視対象銘柄とスナイパーの生成
	type target struct {
		Symbol string
		Qty    int
		Price  float64
	}
	watchList := []target{
		{Symbol: "9433", Qty: 100, Price: 3990.0},
	}

	for _, t := range watchList {
		buyStrategy := strategy.NewLimitBuy(t.Price, t.Qty)
		sellStrategy := strategy.NewFixedRate(t.Price, 0.002, t.Qty)
		masterStrategy := strategy.NewRoundTrip(buyStrategy, sellStrategy)
		safeLogic := strategy.NewKillSwitch(masterStrategy, t.Qty)

		s := sniper.NewSniper(t.Symbol, safeLogic, executor)
		snipers = append(snipers, s)
		watchSymbols = append(watchSymbols, t.Symbol)

		fmt.Printf("🎯 新規配備: %s -> [%.1f円買 -> +0.2%%売]\n", t.Symbol, t.Price)
	}

	// 3. 配信サービスの生成
	var streamer market.PriceStreamer = kabu.NewKabuStreamer("ws://localhost:18080/kabusapi/websocket")

	// 4. ユースケースの生成（★ここが追加部分）
	tradeUC := usecase.NewTradeUseCase(snipers)
	lifecycleUC := usecase.NewLifecycleUseCase(snipers, client, apiPassword)

	// 5. 司令部（Engine）の生成
	return NewEngine(streamer, tradeUC, lifecycleUC, watchSymbols)
}
