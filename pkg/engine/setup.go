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
func BuildEngine(ctx context.Context, cfg *config.AppConfig, targets []portfolio.SymbolTarget, opTargets []portfolio.OperationTarget) (*Engine, error) {
	// 1. インフラ層の構築（泥臭い設定はすべてここへ）
	gateway, err := buildInfrastructure(cfg)
	if err != nil {
		return nil, err
	}

	// 2. 監視リスト (WatchTarget) の自動構築
	watchList, err := buildWatchListFromOperations(ctx, gateway, targets, opTargets)
	if err != nil {
		return nil, err
	}

	// 3. ドメイン層（スナイパー）の配備（DataPoolはGatewayから直接もらう！）
	snipers, err := deploySnipers(watchList, gateway.DataPool())
	if err != nil {
		return nil, fmt.Errorf("スナイパーの配備に失敗: %w", err)
	}

	// 4. 作戦（Operation）の構築
	operations := buildOperationsFromConfigs(gateway.DataPool(), snipers, opTargets)

	tradeUC := usecase.NewTradeUseCase(operations, gateway)
	systemUC := usecase.NewSystemUseCase(operations, gateway)
	handler := usecase.NewUseCaseHandler(systemUC, tradeUC)

	// 5. エンジンの完成
	return NewEngine(handler), nil
}

// ---------------------------------------------------------
// ▼ ここから下は「下請け工場（プライベート関数）」に押し込む
// ---------------------------------------------------------

// buildWatchListFromOperations はマスタ登録情報と作戦設定を突合し、監視すべき WatchTarget リストを自動構築します。
func buildWatchListFromOperations(
	ctx context.Context,
	gateway market.MarketGateway,
	targets []portfolio.SymbolTarget,
	opTargets []portfolio.OperationTarget,
) ([]symbol.WatchTarget, error) {
	// 有効化されたマスタ銘柄マップの構築
	enabledAssets := make(map[string]portfolio.SymbolTarget)
	for _, t := range targets {
		if t.Enabled {
			enabledAssets[t.Symbol] = t
		}
	}

	var watchList []symbol.WatchTarget

	for _, op := range opTargets {
		switch op.Type {
		case "default":
			symbolCode, _ := op.Params["symbol"].(string)
			strategiesRaw, _ := op.Params["strategies"].([]interface{})
			strategyParams, _ := op.Params["strategy_params"].(map[string]interface{})

			asset, ok := enabledAssets[symbolCode]
			if !ok {
				slog.Warn("作戦で使用される銘柄が無効またはマスタ未登録です。作戦をスキップします", slog.String("opID", op.ID), slog.String("symbol", symbolCode))
				continue
			}

			detail, err := gateway.GetSymbol(ctx, symbolCode, asset.Exchange)
			if err != nil {
				return nil, err
			}

			for _, stratRaw := range strategiesRaw {
				stratName, _ := stratRaw.(string)
				var params interface{}
				if strategyParams != nil {
					params = strategyParams[stratName]
				}

				watchList = append(watchList, symbol.WatchTarget{
					Detail:       detail,
					StrategyName: stratName,
					Exchange:     asset.Exchange,
					Params:       params,
				})
			}

		case "pair_trading":
			symbolA, _ := op.Params["symbol_a"].(string)
			symbolB, _ := op.Params["symbol_b"].(string)

			assetA, okA := enabledAssets[symbolA]
			assetB, okB := enabledAssets[symbolB]
			if !okA || !okB {
				slog.Warn("ペアトレードに必要な銘柄が無効またはマスタ未登録です。作戦をスキップします", slog.String("opID", op.ID), slog.String("symbolA", symbolA), slog.String("symbolB", symbolB))
				continue
			}

			detailA, err := gateway.GetSymbol(ctx, symbolA, assetA.Exchange)
			if err != nil {
				return nil, err
			}
			detailB, err := gateway.GetSymbol(ctx, symbolB, assetB.Exchange)
			if err != nil {
				return nil, err
			}

			watchList = append(watchList, symbol.WatchTarget{
				Detail:       detailA,
				StrategyName: "pair_trading",
				Exchange:     assetA.Exchange,
				Params:       op.Params,
			})
			watchList = append(watchList, symbol.WatchTarget{
				Detail:       detailB,
				StrategyName: "pair_trading",
				Exchange:     assetB.Exchange,
				Params:       op.Params,
			})
		}
	}

	return watchList, nil
}

// buildOperationsFromConfigs はデプロイされたスナイパーを作戦構成に従って適切にグループ化・割り当てし、Operation一覧を構築します。
func buildOperationsFromConfigs(
	dataPool tick.DataPool,
	snipers []*sniper.Sniper,
	opTargets []portfolio.OperationTarget,
) []sniper.Operation {
	var operations []sniper.Operation
	snipersBySymbol := make(map[string][]*sniper.Sniper)
	pairSnipersBySymbol := make(map[string]*sniper.Sniper)

	for _, s := range snipers {
		if s.Strategy.Name() == "InstructionStrategy" {
			pairSnipersBySymbol[s.Detail.Code] = s
		} else {
			snipersBySymbol[s.Detail.Code] = append(snipersBySymbol[s.Detail.Code], s)
		}
	}

	// operations.json から明示的に Operation を組み立てる
	for _, op := range opTargets {
		switch op.Type {
		case "default":
			symbolCode, _ := op.Params["symbol"].(string)
			symSnipers, ok := snipersBySymbol[symbolCode]
			if ok && len(symSnipers) > 0 {
				nest := buildNestHelper(symbolCode, symSnipers)
				operations = append(operations, sniper.NewDefaultOperation(op.ID, nest))
				delete(snipersBySymbol, symbolCode)
			}

		case "pair_trading":
			symbolA, _ := op.Params["symbol_a"].(string)
			symbolB, _ := op.Params["symbol_b"].(string)
			threshold, _ := op.Params["threshold"].(float64)
			qty, _ := op.Params["qty"].(float64)

			sniperA, okA := pairSnipersBySymbol[symbolA]
			sniperB, okB := pairSnipersBySymbol[symbolB]

			if okA && okB {
				nestA := buildNestHelper(symbolA, []*sniper.Sniper{sniperA})
				nestB := buildNestHelper(symbolB, []*sniper.Sniper{sniperB})

				stratA := sniperA.Strategy.(*sniper.InstructionStrategy)
				stratB := sniperB.Strategy.(*sniper.InstructionStrategy)

				pairOp := sniper.NewPairTradingOperation(
					op.ID, nestA, nestB, stratA, stratB, dataPool, threshold, qty, sniperA.Logger,
				)
				operations = append(operations, pairOp)

				slog.Info("ペアトレード作戦を構築しました", slog.String("opID", op.ID), slog.String("symbolA", symbolA), slog.String("symbolB", symbolB))
			} else {
				slog.Warn("ペアトレードに必要なスナイパーが不足しています", slog.String("symbolA", symbolA), slog.String("symbolB", symbolB))
			}
		}
	}

	// 未配備の「はぐれスナイパー」を自動救済するフォールバック配備（セーフティネット）
	for symbol, symSnipers := range snipersBySymbol {
		nest := buildNestHelper(symbol, symSnipers)
		opID := fmt.Sprintf("FallbackOp_%s", symbol)
		operations = append(operations, sniper.NewDefaultOperation(opID, nest))
		slog.Warn("作戦に未登録のスナイパーをフォールバック作戦として自動配備しました", slog.String("symbol", symbol))
	}

	return operations
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

func buildNestHelper(symbol string, symSnipers []*sniper.Sniper) *sniper.SniperNest {
	var spotter *sniper.Spotter
	if len(symSnipers) > 0 {
		spotter = sniper.NewSpotter(symSnipers[0].Detail, symSnipers[0].Logger)
	}
	return sniper.NewSniperNest(symbol, spotter, symSnipers)
}


