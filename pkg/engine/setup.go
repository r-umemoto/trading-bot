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

	// 4. 狙撃陣地（SniperNest）および 作戦（Operation）の構築
	var operations []sniper.Operation
	snipersBySymbol := make(map[string][]*sniper.Sniper)
	for _, s := range snipers {
		snipersBySymbol[s.Detail.Code] = append(snipersBySymbol[s.Detail.Code], s)
	}

	// portfolio のパラメータ依存でペアトレードを構築する
	for _, t := range targets {
		hasPairTrading := false
		for _, s := range t.Strategies {
			if s == "pair_trading" {
				hasPairTrading = true
				break
			}
		}
		if !hasPairTrading {
			continue
		}

		p, err := parsePairTradingParams(t.Params)
		if err != nil {
			slog.Warn("ペアトレードパラメータのパースに失敗", slog.String("symbol", t.Symbol), slog.Any("error", err))
			continue
		}

		// プライマリ側でのみ Operation を構築する
		if !p.IsPrimary {
			continue
		}

		symbolA := t.Symbol
		symbolB := p.Partner

		snipersA, okA := snipersBySymbol[symbolA]
		snipersB, okB := snipersBySymbol[symbolB]

		if okA && okB && len(snipersA) > 0 && len(snipersB) > 0 {
			nestA := buildNestHelper(symbolA, snipersA)
			nestB := buildNestHelper(symbolB, snipersB)

			// 両スナイパーの戦略を InstructionStrategy にキャストして差し替え
			var stratA *sniper.InstructionStrategy
			var stratB *sniper.InstructionStrategy

			if sa, ok := snipersA[0].Strategy.(*sniper.InstructionStrategy); ok {
				stratA = sa
			} else {
				stratA = sniper.NewInstructionStrategy()
				snipersA[0].Strategy = stratA
			}

			if sb, ok := snipersB[0].Strategy.(*sniper.InstructionStrategy); ok {
				stratB = sb
			} else {
				stratB = sniper.NewInstructionStrategy()
				snipersB[0].Strategy = stratB
			}

			opID := fmt.Sprintf("PairOp_%s_%s", symbolA, symbolB)
			pairOp := sniper.NewPairTradingOperation(
				opID, nestA, nestB, stratA, stratB, gateway.DataPool(), p.Threshold, p.Qty, snipersA[0].Logger,
			)
			operations = append(operations, pairOp)

			slog.Info("ペアトレード作戦を構築しました", slog.String("opID", opID), slog.String("symbolA", symbolA), slog.String("symbolB", symbolB))

			delete(snipersBySymbol, symbolA)
			delete(snipersBySymbol, symbolB)
		} else {
			slog.Warn("ペアトレードに必要なスナイパーが不足しています", slog.String("symbolA", symbolA), slog.String("symbolB", symbolB))
		}
	}

	for symbol, symSnipers := range snipersBySymbol {
		nest := buildNestHelper(symbol, symSnipers)
		opID := fmt.Sprintf("Op_%s", symbol)
		operations = append(operations, sniper.NewDefaultOperation(opID, nest))
	}

	tradeUC := usecase.NewTradeUseCase(operations, gateway)
	systemUC := usecase.NewSystemUseCase(operations, gateway)
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

func buildNestHelper(symbol string, symSnipers []*sniper.Sniper) *sniper.SniperNest {
	var spotter *sniper.Spotter
	if len(symSnipers) > 0 {
		spotter = sniper.NewSpotter(symSnipers[0].Detail, symSnipers[0].Logger)
	}
	return sniper.NewSniperNest(symbol, spotter, symSnipers)
}

type PairTradingParams struct {
	Partner   string
	Threshold float64
	Qty       float64
	IsPrimary bool
}

func parsePairTradingParams(params map[string]interface{}) (PairTradingParams, error) {
	var p PairTradingParams
	raw, ok := params["pair_trading"]
	if !ok {
		return p, fmt.Errorf("pair_trading params missing")
	}
	m, ok := raw.(map[string]interface{})
	if !ok {
		return p, fmt.Errorf("pair_trading params is not a map")
	}

	if partner, ok := m["partner"].(string); ok {
		p.Partner = partner
	}
	if threshold, ok := m["threshold"].(float64); ok {
		p.Threshold = threshold
	}
	if qty, ok := m["qty"].(float64); ok {
		p.Qty = qty
	}
	if isPrimary, ok := m["is_primary"].(bool); ok {
		p.IsPrimary = isPrimary
	}
	return p, nil
}
