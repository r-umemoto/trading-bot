package runner

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/service"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
	"github.com/r-umemoto/trading-bot/pkg/infra/backtest"
	"github.com/r-umemoto/trading-bot/pkg/portfolio"
	"github.com/r-umemoto/trading-bot/pkg/usecase"
)

// RunBacktest はバックテストの初期化と実行をカプセル化した関数です。
func RunBacktest() error {
	var csvPath string
	flag.StringVar(&csvPath, "csv", "./data/all_20260409.csv", "バックテスト用CSVファイルのパス")
	var portfolioPath string
	flag.StringVar(&portfolioPath, "portfolio", "./configs/portfolio.json", "ポートフォリオJSONファイルのパス")
	var execModelStr string
	flag.StringVar(&execModelStr, "execution-model", "pessimistic", "約定モデル (touch, pessimistic, volume)")
	var latencyMs int
	flag.IntVar(&latencyMs, "latency", 0, "発注・キャンセル遅延時間 (ミリ秒)")
	flag.Parse()

	execModel := backtest.ExecutionModel(execModelStr)
	latency := time.Duration(latencyMs) * time.Millisecond

	fmt.Printf("戦略のバックテストを開始します... (データ: %s, 約定モデル: %s, 遅延: %v)\n", csvPath, execModel, latency)

	// 2. 監視銘柄（Sniper）のセットアップ
	targets, err := portfolio.LoadFromJSON(portfolioPath)
	if err != nil {
		return fmt.Errorf("ポートフォリオの読み込みに失敗しました: %w", err)
	}

	// 3. バックテスト用インフラ（Mock Gateway）の準備
	gateway := backtest.NewSyncBacktestGateway(execModel, latency)
	dataPool := gateway.DataPool()
	_, _ = gateway.Listen(context.Background())
	tickCh := gateway.TickCh()
	orderReportCh := gateway.OrderCh()

	// 4. 監視リストの構築 (Gatewayを使用して情報を埋める)
	watchList, err := portfolio.BuildWatchList(context.Background(), gateway, targets)
	if err != nil {
		return err
	}

	// バックテストログディレクトリの準備
	logDir := filepath.Join("backtest_logs", time.Now().Format("20060102_150405"))
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return fmt.Errorf("バックテストログディレクトリの作成に失敗: %w", err)
	}

	// 4. スナイパーの配備
	var snipers []*sniper.Sniper
	snipersBySymbol := make(map[string][]*sniper.Sniper)

	for _, sym := range watchList {
		factory, err := strategy.GetFactory(sym.StrategyName)
		if err != nil {
			return fmt.Errorf("戦略 '%s' が見つかりません: %w", sym.StrategyName, err)
		}
		st := factory.NewStrategy(sym.Detail, dataPool, sym.Params)
		policy := factory.CreateExecutionPolicy(sym.Params)

		// 銘柄別のロガーを生成
		logPath := filepath.Join(logDir, fmt.Sprintf("%s_%s.jsonl", sym.Detail.Code, sym.StrategyName))
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		var analysisLogger *slog.Logger
		if err == nil {
			analysisLogger = slog.New(slog.NewJSONHandler(f, nil))
		} else {
			slog.Error("バックテストログファイルの作成に失敗", slog.String("path", logPath), slog.Any("error", err))
		}

		sniperID := fmt.Sprintf("%s_%s", sym.StrategyName, sym.Detail.Code)
		s := sniper.NewSniper(sniperID, sym.Detail, st, policy, sym.Exchange, analysisLogger)
		snipers = append(snipers, s)
		snipersBySymbol[sym.Detail.Code] = append(snipersBySymbol[sym.Detail.Code], s)
	}

	// 5. 陣地（Nest）および 作戦（Operation）の構築
	var operations []sniper.Operation

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
			// spotter の生成
			spotterA := sniper.NewSpotter(snipersA[0].Detail, snipersA[0].Logger)
			spotterB := sniper.NewSpotter(snipersB[0].Detail, snipersB[0].Logger)

			nestA := sniper.NewSniperNest(symbolA, spotterA, snipersA)
			nestB := sniper.NewSniperNest(symbolB, spotterB, snipersB)

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
				opID, nestA, nestB, stratA, stratB, dataPool, p.Threshold, p.Qty, snipersA[0].Logger,
			)
			operations = append(operations, pairOp)

			slog.Info("バックテスト用ペアトレード作戦を構築しました", slog.String("opID", opID), slog.String("symbolA", symbolA), slog.String("symbolB", symbolB))

			delete(snipersBySymbol, symbolA)
			delete(snipersBySymbol, symbolB)
		} else {
			slog.Warn("バックテスト用ペアトレードに必要なスナイパーが不足しています", slog.String("symbolA", symbolA), slog.String("symbolB", symbolB))
		}
	}

	// 残りの銘柄を単一銘柄 Operation として構築する
	for symbol, symSnipers := range snipersBySymbol {
		var spotter *sniper.Spotter
		if len(symSnipers) > 0 {
			spotter = sniper.NewSpotter(symSnipers[0].Detail, symSnipers[0].Logger)
		}
		nest := sniper.NewSniperNest(symbol, spotter, symSnipers)
		opID := fmt.Sprintf("Op_%s", symbol)
		operations = append(operations, sniper.NewDefaultOperation(opID, nest))
	}

	// PositionCleaner の起動 (Gatewayに依存するため)
	cleanableTargets := make([]usecase.CleanableTarget, len(snipers))
	for i, s := range snipers {
		cleanableTargets[i] = s
	}
	_ = usecase.NewPositionCleaner(cleanableTargets, gateway)

	// 5. Feederの準備
	csvTickChan := make(chan tick.Tick, 1000)

	// Feederを別ゴルーチンで起動し、CSVの読み込みを開始
	go func() {
		if err := runCustomCSVFeeder(csvPath, csvTickChan); err != nil {
			fmt.Printf("Feeder実行エラー: %v\n", err)
		}
	}()

	tickCount := 0

	// 6. メインループ
	for tick := range csvTickChan {
		tickCount++

		gateway.ProcessTick(tick)

		for len(orderReportCh) > 0 {
			report := <-orderReportCh
			for _, op := range operations {
				op.UpdateOrders(report)
			}
		}

		t := <-tickCh

		for _, op := range operations {
			hasSymbol := false
			for _, code := range op.GetSymbolCodes() {
				if code == t.Symbol {
					hasSymbol = true
					break
				}
			}
			if !hasSymbol {
				continue
			}

			actions := op.HandleTick(t)

			for _, act := range actions {
				bullet := act.Bullet
				if bullet.HasCancel() {
					fmt.Printf("🛑 [Backtest] 自動キャンセルを実行: %s\n", bullet.CancelOrderID)
					_ = gateway.CancelOrder(context.Background(), bullet.CancelOrderID)
				}

				if bullet.HasOrder() {
					updatedOrder, err := gateway.SendOrder(context.Background(), order.SendOrderInput{Order: bullet.Order, Request: *bullet.Request})
					if err != nil {
						op.FailSendingOrder(act.SniperID, bullet.Order)
					} else {
						op.UpdateOrderID(act.SniperID, bullet.Order, updatedOrder.ID)
					}
				}
			}
		}

		if tickCount%100000 == 0 {
			fmt.Printf("%d件のTickを処理完了...\n", tickCount)
		}
	}

	// 7. 取引終了後のクリーンアップ
	ordersToMopUp, _ := gateway.GetOrders(context.Background())
	fmt.Printf("クリーンアップ開始: 未約定の可能性がある注文数 %d件\n", len(ordersToMopUp.Orders))
	for _, o := range ordersToMopUp.Orders {
		if !o.IsCompleted() {
			state := dataPool.GetState(o.Symbol)
			if !state.LatestTick.CurrentPriceTime.IsZero() {
				gateway.ProcessTick(state.LatestTick)
				for len(orderReportCh) > 0 {
					report := <-orderReportCh
					for _, op := range operations {
						op.UpdateOrders(report)
					}
				}
				t := <-tickCh
				for _, op := range operations {
					hasSymbol := false
					for _, code := range op.GetSymbolCodes() {
						if code == t.Symbol {
							hasSymbol = true
							break
						}
					}
					if hasSymbol {
						_ = op.HandleTick(t)
					}
				}
			}
		}
	}

	time.Sleep(100 * time.Millisecond)

	fmt.Printf("バックテスト完了: 総処理Tick数 %d件\n", tickCount)

	// 結果の出力
	positions, _ := gateway.GetPositions(context.Background(), order.PRODICT_CASH)
	ords, _ := gateway.GetOrders(context.Background())
	fmt.Println("\n=============================================")
	fmt.Println("             バックテスト結果")
	fmt.Println("=============================================")
	for _, p := range positions {
		fmt.Printf("最終建玉: 銘柄 %s, 数量 %.f\n", p.Symbol, p.LeavesQty)
	}
	fmt.Printf("総発注数: %d件\n", len(ords.Orders))

	// 結果の出力
	provider := &backtestPerformanceProvider{operations: operations}
	reportTargets := make([]sniper.ReportableTarget, 0)
	for _, op := range operations {
		reportTargets = append(reportTargets, op.GetReportableTargets()...)
	}
	report := service.GeneratePerformanceReport(provider, reportTargets, gateway.DataPool())
	presenter := usecase.NewReportPresenter()
	presenter.PrintPerformanceReport(report, false)

	return nil
}

type backtestPerformanceProvider struct {
	operations []sniper.Operation
}

func (p *backtestPerformanceProvider) GetPerformance(sniperID string) sniper.Performance {
	for _, op := range p.operations {
		if op.HasSniper(sniperID) {
			return op.GetPerformance(sniperID)
		}
	}
	return sniper.Performance{}
}

func (p *backtestPerformanceProvider) GetUnrealizedPnL(sniperID string, currentPrice float64) float64 {
	for _, op := range p.operations {
		if op.HasSniper(sniperID) {
			return op.GetUnrealizedPnL(sniperID, currentPrice)
		}
	}
	return 0
}

func runCustomCSVFeeder(csvPath string, tickChan chan<- tick.Tick) error {
	// 🌟 CSVファイル名から日付 (YYYYMMDD) を抽出。デフォルトは実行当日の日付
	baseDate := time.Now()
	baseName := filepath.Base(csvPath)
	for i := 0; i <= len(baseName)-8; i++ {
		sub := baseName[i : i+8]
		if _, err := strconv.Atoi(sub); err == nil {
			if d, err := time.Parse("20060102", sub); err == nil {
				baseDate = d
				break
			}
		}
	}

	file, err := os.Open(csvPath)
	if err != nil {
		return err
	}
	defer file.Close()

	reader := csv.NewReader(file)

	if _, err := reader.Read(); err != nil {
		return err
	}

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		parsedTime, _ := time.Parse("15:04:05.000", record[0])
		// 🌟 抽出した日付と時刻をマージして完全な time.Time を生成
		tickTime := time.Date(
			baseDate.Year(), baseDate.Month(), baseDate.Day(),
			parsedTime.Hour(), parsedTime.Minute(), parsedTime.Second(), parsedTime.Nanosecond(),
			time.Local,
		)
		price, _ := strconv.ParseFloat(record[2], 64)
		volume, _ := strconv.ParseFloat(record[3], 64)
		vwap, _ := strconv.ParseFloat(record[4], 64)

		// 板情報のパース (最良気配)
		askPrice, _ := strconv.ParseFloat(record[5], 64)
		askQty, _ := strconv.ParseFloat(record[6], 64)
		bidPrice, _ := strconv.ParseFloat(record[7], 64)
		bidQty, _ := strconv.ParseFloat(record[8], 64)

		var sellBoard []tick.Quote
		var buyBoard []tick.Quote
		statusIdx := 9 // デフォルト（旧フォーマット）

		// フル板情報がある場合 (新フォーマット)
		if len(record) >= 46 {
			statusIdx = 45
			for i := 0; i < 9; i++ {
				base := 9 + (i * 4)
				askP, _ := strconv.ParseFloat(record[base], 64)
				askQ, _ := strconv.ParseFloat(record[base+1], 64)
				bidP, _ := strconv.ParseFloat(record[base+2], 64)
				bidQ, _ := strconv.ParseFloat(record[base+3], 64)

				if askP > 0 {
					sellBoard = append(sellBoard, tick.Quote{Price: askP, Qty: askQ})
				}
				if bidP > 0 {
					buyBoard = append(buyBoard, tick.Quote{Price: bidP, Qty: bidQ})
				}
			}
		}

		status := 1
		if len(record) > statusIdx {
			if s, err := strconv.Atoi(record[statusIdx]); err == nil {
				status = s
			}
		}

		tick := tick.Tick{
			Symbol:        record[1],
			Price:         price,
			TradingVolume: volume,
			VWAP:          vwap,
			BestAsk: tick.FirstQuote{
				Price: askPrice,
				Qty:   askQty,
			},
			BestBid: tick.FirstQuote{
				Price: bidPrice,
				Qty:   bidQty,
			},
			SellBoard:          sellBoard,
			BuyBoard:           buyBoard,
			CurrentPriceTime:   tickTime,
			CurrentPriceStatus: tick.PriceStatus(status),
		}

		tickChan <- tick
	}

	close(tickChan)
	return nil
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
