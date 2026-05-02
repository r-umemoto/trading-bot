// cmd/bot/portfolio.go
package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/r-umemoto/trading-bot/pkg/config"
	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/service"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
	"github.com/r-umemoto/trading-bot/pkg/infra/kabu"
	"github.com/r-umemoto/trading-bot/pkg/infra/kabu/api"
	"github.com/r-umemoto/trading-bot/pkg/portfolio"
	"github.com/r-umemoto/trading-bot/pkg/usecase"
)

// BuildEngine は、システム全体を俯瞰する「目次」です
func BuildEngine(ctx context.Context, cfg *config.AppConfig, targets []portfolio.SymbolTarget) (*Engine, error) {
	// 1. インフラ層の構築（泥臭い設定はすべてここへ）
	gateway, err := buildInfrastructure(cfg)
	if err != nil {
		return nil, err
	}

	// 2. 監視リストの構築 (ここでAPIを叩いて銘柄詳細を取得)
	watchList, err := portfolio.BuildWatchList(ctx, gateway, targets)
	if err != nil {
		return nil, err
	}

	// 3. ユースケースとサービスの組み立て用のDataPool準備
	dataPool := market.NewDefaultDataPool()

	// 4. ドメイン層（スナイパー）の配備
	snipers, watchSymbols, err := deploySnipers(watchList, dataPool)
	if err != nil {
		return nil, fmt.Errorf("スナイパーの配備に失敗: %w", err)
	}

	tradeUC := usecase.NewTradeUseCase(snipers, gateway, dataPool)
	cleaner := service.NewPositionCleaner(snipers, gateway)

	// 5. エンジンの完成
	return NewEngine(gateway, tradeUC, cleaner, watchSymbols), nil
}

// ---------------------------------------------------------
// ▼ ここから下は「下請け工場（プライベート関数）」に押し込む
// ---------------------------------------------------------

func buildInfrastructure(cfg *config.AppConfig) (market.MarketGateway, error) {
	if cfg.BrokerType != "kabu" {
		return nil, fmt.Errorf("未対応のブローカーです: %s", cfg.BrokerType)
	}

	client := api.NewKabuClient(cfg.Kabu)
	if err := client.GetToken(); err != nil {
		return nil, fmt.Errorf("トークン取得エラー: %w", err)
	}

	wsURL := strings.Replace(cfg.Kabu.APIURL, "http://", "ws://", 1) + "/websocket"
	wsClient := api.NewWSClient(wsURL)

	// 統合された KabuMarket を生成
	marketGateway := kabu.NewMarketGateway(client, wsClient)

	return marketGateway, nil
}

func deploySnipers(watchList []market.WatchTarget, dataPool market.DataPool) ([]*sniper.Sniper, []string, error) {
	var snipers []*sniper.Sniper
	var watchSymbols []string
	symbolMap := make(map[string]bool)

	for _, t := range watchList {
		factory, err := strategy.GetFactory(t.StrategyName)
		if err != nil {
			return nil, nil, fmt.Errorf("戦略 '%s' が見つかりません: %w", t.StrategyName, err)
		}

		st := factory.NewStrategy(t.Detail.Symbol, dataPool)
		s := sniper.NewSniper(t.Detail, st, t.Exchange)
		snipers = append(snipers, s)

		if !symbolMap[t.Detail.Symbol] {
			symbolMap[t.Detail.Symbol] = true
			watchSymbols = append(watchSymbols, t.Detail.Symbol)
		}
	}

	return snipers, watchSymbols, nil
}
