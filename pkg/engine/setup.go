// cmd/bot/portfolio.go
package engine

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/config"
	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
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

	// 3. ドメイン層（スナイパー）の配備（DataPoolはGatewayから直接もらう！）
	snipers, err := deploySnipers(watchList, gateway.DataPool())
	if err != nil {
		return nil, fmt.Errorf("スナイパーの配備に失敗: %w", err)
	}

	// 4. 狙撃陣地（SniperNest）の構築
	var nests []*sniper.SniperNest
	snipersBySymbol := make(map[string][]*sniper.Sniper)
	for _, s := range snipers {
		snipersBySymbol[s.Detail.Code] = append(snipersBySymbol[s.Detail.Code], s)
	}

	for symbol, symSnipers := range snipersBySymbol {
		var spotter *sniper.Spotter
		if len(symSnipers) > 0 {
			spotter = sniper.NewSpotter(symSnipers[0].Detail, symSnipers[0].Logger)
		}
		nest := sniper.NewSniperNest(symbol, spotter, symSnipers)
		nests = append(nests, nest)
	}

	tradeUC := usecase.NewTradeUseCase(nests, gateway)
	systemUC := usecase.NewSystemUseCase(nests, gateway)
	handler := usecase.NewUseCaseHandler(systemUC, tradeUC)

	// 5. エンジンの完成
	return NewEngine(handler), nil
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

func deploySnipers(watchList []symbol.WatchTarget, dataPool tick.DataPool) ([]*sniper.Sniper, error) {
	var snipers []*sniper.Sniper

	// ログディレクトリの準備
	logDir := filepath.Join("logs", time.Now().Format("20060102"))
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("ログディレクトリの作成に失敗: %w", err)
	}

	for _, t := range watchList {
		factory, err := strategy.GetFactory(t.StrategyName)
		if err != nil {
			return nil, fmt.Errorf("戦略 '%s' が見つかりません: %w", t.StrategyName, err)
		}

		st := factory.NewStrategy(t.Detail, dataPool, t.Params)
		policy := factory.CreateExecutionPolicy(t.Params)

		// 銘柄別のロガーを生成
		logPath := filepath.Join(logDir, fmt.Sprintf("%s_%s.jsonl", t.Detail.Code, t.StrategyName))
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		var analysisLogger *slog.Logger
		if err == nil {
			analysisLogger = slog.New(slog.NewJSONHandler(f, nil))
		} else {
			slog.Error("ログファイルの作成に失敗", slog.String("path", logPath), slog.Any("error", err))
		}

		sniperID := fmt.Sprintf("%s_%s", t.StrategyName, t.Detail.Code)
		s := sniper.NewSniper(sniperID, t.Detail, st, policy, t.Exchange, analysisLogger)
		snipers = append(snipers, s)
	}

	return snipers, nil
}
