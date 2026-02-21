// cmd/bot/portfolio.go
package main

import (
	"fmt"

	"trading-bot/internal/domain/market"
	"trading-bot/internal/domain/sniper"
	"trading-bot/internal/domain/sniper/strategy"
	"trading-bot/internal/infra/kabu"
)

// buildPortfolio は監視対象銘柄と戦略（ビジネスロジック）を組み立て、
// スナイパー部隊と価格配信サービス（Streamer）を生成して返します。
func buildPortfolio(client *kabu.KabuClient, apiPassword string) ([]*sniper.Sniper, market.PriceStreamer) {
	var snipers []*sniper.Sniper

	// 1. カブコム用の発注アダプター（執行者）を生成
	var executor sniper.OrderExecutor = kabu.NewKabuExecutor(client, apiPassword)

	// 2. 監視対象銘柄とパラメータの定義
	type target struct {
		Symbol string
		Qty    int
		Price  float64
	}
	watchList := []target{
		{
			Symbol: "9433", // KDDI
			Qty:    100,    // 100株
			Price:  3990.0, // 3990円
		},
		// 将来、別の銘柄を追加する場合はここに追記するだけでOK
	}

	// 3. スナイパー部隊の編成（戦略の注入）
	for _, t := range watchList {
		// エントリー戦略とエグジット戦略を定義
		buyStrategy := strategy.NewLimitBuy(t.Price, t.Qty)
		sellStrategy := strategy.NewFixedRate(t.Price, 0.002, t.Qty)

		// 包括的戦略（1往復）として束ねる
		masterStrategy := strategy.NewRoundTrip(buyStrategy, sellStrategy)

		// キルスイッチで安全装置を付ける
		safeLogic := strategy.NewKillSwitch(masterStrategy, t.Qty)

		// スナイパーを生成して部隊に追加
		s := sniper.NewSniper(t.Symbol, safeLogic, executor)
		snipers = append(snipers, s)

		fmt.Printf("🎯 新規配備: %s -> [%.1f円買 -> +0.2%%売] (キルスイッチ装備)\n", t.Symbol, t.Price)
	}

	// 4. カブコム用の価格配信サービス（Streamer）を生成
	wsURL := "ws://localhost:18080/kabusapi/websocket"
	var streamer market.PriceStreamer = kabu.NewKabuStreamer(wsURL)

	return snipers, streamer
}
