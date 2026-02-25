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
	gateway, err := buildInfrastructure(cfg)
	if err != nil {
		return nil, err
	}

	// 2. ドメイン層（スナイパー）の配備
	snipers, watchSymbols := deploySnipers()

	// 3. ユースケースとサービスの組み立て
	analyzer := market.NewDefaultAnalyzer()
	tradeUC := usecase.NewTradeUseCase(snipers, gateway, analyzer)
	cleaner := service.NewPositionCleaner(snipers, gateway)

	// 4. エンジンの完成
	return NewEngine(gateway, tradeUC, cleaner, watchSymbols), nil
}

// ---------------------------------------------------------
// ▼ ここから下は「下請け工場（プライベート関数）」に押し込む
// ---------------------------------------------------------

func buildInfrastructure(cfg *config.AppConfig) (market.MarketGateway, error) {
	if cfg.BrokerType != "kabu" {
		return nil, fmt.Errorf("未対応のブローカーです: %s", cfg.BrokerType)
	}

	client := kabu.NewKabuClient(cfg.Kabu)
	if err := client.GetToken(); err != nil {
		return nil, fmt.Errorf("トークン取得エラー: %w", err)
	}

	wsURL := strings.Replace(cfg.Kabu.APIURL, "http://", "ws://", 1) + "/websocket"
	wsClient := kabu.NewWSClient(wsURL)

	// 統合された KabuMarket を生成
	marketGateway := kabu.NewMarketGateway(client, wsClient)

	return marketGateway, nil
}

func deploySnipers() ([]*sniper.Sniper, []string) {
	var snipers []*sniper.Sniper
	var watchSymbols []string

	watchList := []struct {
		Symbol string
		Qty    float64
		Price  float64
	}{
		{Symbol: "9433", Qty: 100, Price: 3990.0},
	}

	for _, t := range watchList {
		vwapStrategy := strategy.NewVWAPReboundStrategy(0.5, 0.1, 100)
		budgetLogic := strategy.NewBudgetConstraint(vwapStrategy, 1000000.0)
		safeLogic := strategy.NewKillSwitch(budgetLogic, t.Qty)

		s := sniper.NewSniper(t.Symbol, safeLogic)
		snipers = append(snipers, s)
		watchSymbols = append(watchSymbols, t.Symbol)
	}

	return snipers, watchSymbols
}
