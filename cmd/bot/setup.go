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

// ... (インポート略) ...

// buildPortfolio は、システム全体を俯瞰する「目次」です
func buildEngine(cfg *config.AppConfig) (*Engine, error) {
	// 1. インフラ層の構築（泥臭い設定はすべてここへ）
	executor, streamer, client, err := buildInfrastructure(cfg)
	if err != nil {
		return nil, err
	}

	// 2. ドメイン層（スナイパー）の配備
	snipers, watchSymbols := deploySnipers(executor)

	// 3. ユースケースとサービスの組み立て
	tradeUC := usecase.NewTradeUseCase(snipers)
	cleaner := service.NewPositionCleaner(snipers, client, cfg.Kabu.Password)

	// 4. エンジンの完成
	return NewEngine(streamer, tradeUC, cleaner, watchSymbols), nil
}

// ---------------------------------------------------------
// ▼ ここから下は「下請け工場（プライベート関数）」に押し込む
// ---------------------------------------------------------

func buildInfrastructure(cfg *config.AppConfig) (sniper.OrderExecutor, market.PriceStreamer, *kabu.KabuClient, error) {
	if cfg.BrokerType != "kabu" {
		return nil, nil, nil, fmt.Errorf("未対応のブローカーです: %s", cfg.BrokerType)
	}

	client := kabu.NewKabuClient(cfg.Kabu)
	if err := client.GetToken(cfg.Kabu.Password); err != nil {
		return nil, nil, nil, fmt.Errorf("トークン取得エラー: %w", err)
	}

	executor := kabu.NewKabuExecutor(client, cfg.Kabu.Password)

	wsURL := strings.Replace(cfg.Kabu.APIURL, "http://", "ws://", 1)
	streamer := kabu.NewKabuStreamer(wsURL + "/websocket")

	return executor, streamer, client, nil
}

func deploySnipers(executor sniper.OrderExecutor) ([]*sniper.Sniper, []string) {
	var snipers []*sniper.Sniper
	var watchSymbols []string

	watchList := []struct {
		Symbol string
		Qty    int
		Price  float64
	}{
		{Symbol: "9433", Qty: 100, Price: 3990.0},
	}

	for _, t := range watchList {
		buyStrategy := strategy.NewLimitBuy(t.Price, t.Qty)
		sellStrategy := strategy.NewFixedRate(t.Price, 0.002, t.Qty)
		masterStrategy := strategy.NewRoundTrip(buyStrategy, sellStrategy)
		budgetLogic := strategy.NewBudgetConstraint(masterStrategy, 1000000.0)
		safeLogic := strategy.NewKillSwitch(budgetLogic, t.Qty)

		s := sniper.NewSniper(t.Symbol, safeLogic, executor)
		snipers = append(snipers, s)
		watchSymbols = append(watchSymbols, t.Symbol)
	}

	return snipers, watchSymbols
}
