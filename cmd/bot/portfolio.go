// cmd/bot/portfolio.go
package main

import (
	"fmt"
	"strings"

	"trading-bot/internal/config"
	"trading-bot/internal/domain/market"
	"trading-bot/internal/domain/service"
	"trading-bot/internal/domain/sniper"
	"trading-bot/internal/domain/sniper/strategy"
	"trading-bot/internal/infra/kabu"
	"trading-bot/internal/usecase"
)

// buildPortfolio はすべての依存関係を解決し、実行可能なEngineを構築します
func buildPortfolio(cfg *config.AppConfig) *Engine {
	var snipers []*sniper.Sniper
	var watchSymbols []string

	// 1. BrokerType に応じてインフラを切り替え
	var executor sniper.OrderExecutor
	var streamer market.PriceStreamer
	var client *kabu.KabuClient

	if cfg.BrokerType == "kabu" {
		// ★ カブコムの初期化には cfg.Kabu だけを渡す
		client = kabu.NewKabuClient(cfg.Kabu)

		// トークン取得やその他の初期化には cfg.Kabu.Password を使う
		if err := client.GetToken(cfg.Kabu.Password); err != nil {
			fmt.Printf("トークン取得エラー: %v\n", err)
		}
		executor = kabu.NewKabuExecutor(client, cfg.Kabu.Password)
		wsURL := strings.Replace(cfg.Kabu.APIURL, "http://", "ws://", 1)
		streamer = kabu.NewKabuStreamer(wsURL + "/websocket")
	} else {
		panic("未対応のブローカーです: " + cfg.BrokerType)
	}

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

	// 4. ユースケースの生成（★ここが追加部分）
	tradeUC := usecase.NewTradeUseCase(snipers)
	cleaner := service.NewPositionCleaner(snipers, client, cfg.Kabu.Password)

	// 5. 司令部（Engine）の生成
	return NewEngine(streamer, tradeUC, cleaner, watchSymbols)
}
