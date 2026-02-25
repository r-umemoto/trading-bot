// cmd/bot/portfolio.go
package engine

import (
	"fmt"
	"strings"
	"trading-bot/pkg/config"
	"trading-bot/pkg/domain/market"
	"trading-bot/pkg/domain/service"
	"trading-bot/pkg/domain/sniper"
	"trading-bot/pkg/domain/sniper/strategy"
	"trading-bot/pkg/infra/kabu"
	"trading-bot/pkg/usecase"
)

// WatchTarget defines what symbol to watch with which strategy.
type WatchTarget struct {
	Symbol       string
	StrategyName string
}

// BuildEngine は、システム全体を俯瞰する「目次」です
func BuildEngine(cfg *config.AppConfig, watchList []WatchTarget) (*Engine, error) {
	// 1. インフラ層の構築（泥臭い設定はすべてここへ）
	gateway, err := buildInfrastructure(cfg)
	if err != nil {
		return nil, err
	}

	// 2. ドメイン層（スナイパー）の配備
	snipers, watchSymbols, err := deploySnipers(watchList)
	if err != nil {
		return nil, fmt.Errorf("スナイパーの配備に失敗: %w", err)
	}

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

func deploySnipers(watchList []WatchTarget) ([]*sniper.Sniper, []string, error) {
	var snipers []*sniper.Sniper
	var watchSymbols []string

	for _, t := range watchList {
		// 戦略レジストリから戦略を取得
		st, err := strategy.Get(t.StrategyName)
		if err != nil {
			return nil, nil, fmt.Errorf("戦略 '%s' が見つかりません: %w", t.StrategyName, err)
		}

		s := sniper.NewSniper(t.Symbol, st)
		snipers = append(snipers, s)
		watchSymbols = append(watchSymbols, t.Symbol)
	}

	return snipers, watchSymbols, nil
}
