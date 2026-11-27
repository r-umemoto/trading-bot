package runner

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/service"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
	"github.com/r-umemoto/trading-bot/pkg/infra/backtest"
	"github.com/r-umemoto/trading-bot/pkg/infra/logger"
	"github.com/r-umemoto/trading-bot/pkg/portfolio"
	"github.com/r-umemoto/trading-bot/pkg/usecase"
)

// RunBacktest はバックテストの初期化と実行をカプセル化した関数です。
func RunBacktest() error {
	var csvPath string
	flag.StringVar(&csvPath, "csv", "./data/all_20260409.csv", "バックテスト用CSVファイルのパス")
	var portfolioPath string
	flag.StringVar(&portfolioPath, "portfolio", "./configs/portfolio.json", "ポートフォリオJSONファイルのパス")
	var operationsPath string
	flag.StringVar(&operationsPath, "operations", "./configs/operations.json", "作戦設定JSONファイルのパス")
	var execModelStr string
	flag.StringVar(&execModelStr, "execution-model", "volume", "約定モデル (touch, pessimistic, volume)")
	var latencyMs int
	flag.IntVar(&latencyMs, "latency", 150, "発注・キャンセル遅延時間 (ミリ秒)")
	var detailedLog bool
	flag.BoolVar(&detailedLog, "detailed-log", false, "詳細なJSONLログをファイル出力する（デフォルト: false）")
	flag.Parse()

	// csvPath がディレクトリの場合は、その中の tick データ (all_*.csv または all.csv) を探索して解決します
	fi, err := os.Stat(csvPath)
	if err == nil && fi.IsDir() {
		matches, err := filepath.Glob(filepath.Join(csvPath, "all_*.csv"))
		if err == nil && len(matches) > 0 {
			csvPath = matches[0]
		} else {
			p := filepath.Join(csvPath, "all.csv")
			if _, err := os.Stat(p); err == nil {
				csvPath = p
			} else {
				return fmt.Errorf("指定されたディレクトリ内にチックデータ(all_*.csv または all.csv)が見つかりません: %s", csvPath)
			}
		}
	}

	execModel := backtest.ExecutionModel(execModelStr)
	if !backtest.IsValidExecutionModel(execModel) {
		return fmt.Errorf("未対応の約定モデルです: %q (利用可能: price, touch, pessimistic, volume)", execModelStr)
	}
	latency := time.Duration(latencyMs) * time.Millisecond

	fmt.Printf("戦略のバックテストを開始します... (データ: %s, 約定モデル: %s, 遅延: %v)\n", csvPath, execModel, latency)

	// 2. ポートフォリオおよび作戦のセットアップ
	targets, err := portfolio.LoadFromJSON(portfolioPath)
	if err != nil {
		return fmt.Errorf("ポートフォリオの読み込みに失敗しました: %w", err)
	}

	opTargets, err := portfolio.LoadOperationsFromJSON(operationsPath)
	if err != nil {
		// operations.json が存在しない場合は空としてフォールバック
		opTargets = nil
	}

	// 2.5 ポートフォリオおよび作戦の整合性バリデーション（API 50銘柄上限、マスタ一致等）
	if err := portfolio.Validate(targets, opTargets); err != nil {
		return fmt.Errorf("設定バリデーションエラー: %w", err)
	}

	// 3. バックテスト用インフラ（Mock Gateway）の準備
	gateway := backtest.NewSyncBacktestGateway(execModel, latency)
	dataPool := gateway.DataPool()

	// 監視銘柄の dataPool に latency インジケーターを登録（戦略からレイテンシを参照可能にする）
	for _, t := range targets {
		dataPool.GetOrCreateIndicator(t.Symbol, "latency", func() tick.Indicator {
			return tick.NewStaticFloatIndicator("latency", float64(latencyMs))
		})
	}

	if _, err := gateway.Listen(context.Background()); err != nil {
		return fmt.Errorf("バックテスト用ゲートウェイのListen開始に失敗: %w", err)
	}
	tickCh := gateway.TickCh()
	orderReportCh := gateway.OrderCh()

	// 2. 有効化されたマスタ銘柄マップの構築と呼値グループ（PriceRangeGroup）の登録
	enabledAssets := make(map[string]portfolio.SymbolTarget)
	for _, t := range targets {
		if t.Enabled {
			enabledAssets[t.Symbol] = t
		}
	}
	inferredGroups := inferPriceRangeGroupsFromCSV(csvPath, enabledAssets)
	for code, asset := range enabledAssets {
		prg := asset.PriceRangeGroup
		if prg == 0 {
			if inferred, ok := inferredGroups[code]; ok {
				prg = inferred
			} else {
				prg = symbol.PRICE_RANGE_GROUP_TSE_STANDARD
			}
		}
		gateway.SetSymbol(symbol.Symbol{
			Code:            code,
			Name:            asset.Name,
			PriceRangeGroup: prg,
		})
	}

	// 3. 監視リスト (watchList) の自動構築
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

			detail, err := gateway.GetSymbol(context.Background(), symbolCode, asset.Exchange)
			if err != nil {
				return err
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

			detailA, err := gateway.GetSymbol(context.Background(), symbolA, assetA.Exchange)
			if err != nil {
				return err
			}
			detailB, err := gateway.GetSymbol(context.Background(), symbolB, assetB.Exchange)
			if err != nil {
				return err
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

	// バックテストログディレクトリおよび非同期ライターの準備
	var logDir string
	var asyncWriters []*logger.AsyncWriter
	if detailedLog {
		logDir = filepath.Join("backtest_logs", time.Now().Format("20060102_150405"))
		if err := os.MkdirAll(logDir, 0755); err != nil {
			return fmt.Errorf("バックテストログディレクトリの作成に失敗: %w", err)
		}
		defer func() {
			for _, aw := range asyncWriters {
				_ = aw.Close()
			}
		}()
	}

	// 4. スナイパーの配備
	var snipers []*sniper.Sniper
	snipersBySymbol := make(map[string][]*sniper.Sniper)
	pairSnipersBySymbol := make(map[string]*sniper.Sniper)

	for _, sym := range watchList {
		factory, err := strategy.GetFactory(sym.StrategyName)
		if err != nil {
			return fmt.Errorf("戦略 '%s' が見つかりません: %w", sym.StrategyName, err)
		}
		st := factory.NewStrategy(sym.Detail, dataPool, sym.Params)
		policy := factory.CreateExecutionPolicy(sym.Params)

		// 銘柄別のロガーを生成（デフォルトはio.DiscardでディスクI/Oを完全抑止、-detailed-log時のみ非同期バッファリング出力）
		var analysisLogger *slog.Logger
		if detailedLog {
			logPath := filepath.Join(logDir, fmt.Sprintf("%s_%s.jsonl", sym.Detail.Code, sym.StrategyName))
			f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err == nil {
				aw := logger.NewAsyncWriter(f, 16384)
				asyncWriters = append(asyncWriters, aw)
				analysisLogger = slog.New(slog.NewJSONHandler(aw, nil))
			} else {
				slog.Error("バックテストログファイルの作成に失敗", slog.String("path", logPath), slog.Any("error", err))
				analysisLogger = slog.New(slog.NewJSONHandler(io.Discard, nil))
			}
		} else {
			analysisLogger = slog.New(slog.NewJSONHandler(io.Discard, nil))
		}

		sniperID := fmt.Sprintf("%s_%s", sym.StrategyName, sym.Detail.Code)
		s := sniper.NewSniper(sniperID, sym.Detail, st, policy, sym.Exchange, analysisLogger)
		snipers = append(snipers, s)
		if s.Strategy.Name() == "InstructionStrategy" {
			pairSnipersBySymbol[s.Detail.Code] = s
		} else {
			snipersBySymbol[s.Detail.Code] = append(snipersBySymbol[s.Detail.Code], s)
		}
	}

	// 5. 陣地（Nest）および 作戦（Operation）の構築
	var operations []sniper.Operation

	// operations.json から明示的に Operation を組み立てる
	for _, op := range opTargets {
		switch op.Type {
		case "default":
			symbolCode, _ := op.Params["symbol"].(string)
			symSnipers, ok := snipersBySymbol[symbolCode]
			if ok && len(symSnipers) > 0 {
				nest := sniper.NewSniperNest(symbolCode, symSnipers[0].Detail, symSnipers, symSnipers[0].Logger)
				if modeStr, ok := op.Params["reconciliation_mode"].(string); ok && modeStr == "aggregated" {
					nest.SetReconciliationMode(sniper.ReconciliationModeAggregate)
				}
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
				nestA := sniper.NewSniperNest(symbolA, sniperA.Detail, []*sniper.Sniper{sniperA}, sniperA.Logger)
				nestB := sniper.NewSniperNest(symbolB, sniperB.Detail, []*sniper.Sniper{sniperB}, sniperB.Logger)

				stratA := sniperA.Strategy.(*sniper.InstructionStrategy)
				stratB := sniperB.Strategy.(*sniper.InstructionStrategy)

				pairOp := sniper.NewPairTradingOperation(
					op.ID, nestA, nestB, stratA, stratB, dataPool, threshold, qty, sniperA.Logger,
				)
				operations = append(operations, pairOp)

				slog.Info("バックテスト用ペアトレード作戦を構築しました", slog.String("opID", op.ID), slog.String("symbolA", symbolA), slog.String("symbolB", symbolB))
			} else {
				slog.Warn("バックテスト用ペアトレードに必要なスナイパーが不足しています", slog.String("symbolA", symbolA), slog.String("symbolB", symbolB))
			}
		}
	}

	for symbol, symSnipers := range snipersBySymbol {
		nest := sniper.NewSniperNest(symbol, symSnipers[0].Detail, symSnipers, symSnipers[0].Logger)
		opID := fmt.Sprintf("FallbackOp_%s", symbol)
		operations = append(operations, sniper.NewDefaultOperation(opID, nest))
		slog.Warn("作戦に未登録のスナイパーをフォールバック作戦として自動配備しました", slog.String("symbol", symbol))
	}

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
				if act.Bullet == nil {
					continue
				}
				switch b := act.Bullet.(type) {
				case sniper.CancelBullet:
					fmt.Printf("🛑 [Backtest] 自動キャンセルを実行: %s\n", b.OrderID)
					if err := gateway.CancelOrder(context.Background(), b.OrderID); err != nil {
						op.FailCancelingOrder(act.SniperID, b.OrderID)
					}
				case sniper.OrderBullet:
					fmt.Printf("🔫 [Backtest] 発注: %s %v %.0f株 (Price: %.1f, CashMargin: %v, Reason: %s)\n",
						b.Order.Symbol, b.Order.Action, b.Order.OrderQty, b.Order.OrderPrice, b.Order.CashMargin, b.Order.Reason)
					updatedOrder, err := gateway.SendOrder(context.Background(), order.SendOrderInput{Order: b.Order})
					if err != nil {
						op.FailSendingOrder(act.SniperID, b.Order)
					} else {
						op.UpdateOrderID(act.SniperID, b.Order, updatedOrder.ID)
					}
				}
			}
		}

		if tickCount%100000 == 0 {
			fmt.Printf("%d件のTickを処理完了...\n", tickCount)
		}
	}

	// 7. 取引終了後の残存約定レポートの完全フラッシュと成行決済の引け成り確定
	for len(orderReportCh) > 0 {
		report := <-orderReportCh
		for _, op := range operations {
			op.UpdateOrders(report)
		}
	}

	// 大引け時点で未約定の成行注文や未決済建玉がある場合、最終Tickで引け成り約定させる
	for i := 0; i < 3; i++ {
		ordersToMopUp, _ := gateway.GetOrders(context.Background())
		hasPending := false
		for _, o := range ordersToMopUp.Orders {
			if !o.IsCompleted() && o.IsMarket() {
				hasPending = true
				state := dataPool.GetState(o.Symbol)
				if !state.LatestTick.CurrentPriceTime.IsZero() {
					gateway.ProcessTick(state.LatestTick)
				}
			}
		}

		for len(orderReportCh) > 0 {
			report := <-orderReportCh
			for _, op := range operations {
				op.UpdateOrders(report)
			}
		}

		if !hasPending {
			break
		}
	}

	time.Sleep(50 * time.Millisecond)

	fmt.Printf("バックテスト完了: 総処理Tick数 %d件\n", tickCount)

	// 結果の出力
	positions, err := gateway.GetPositions(context.Background(), order.PRODUCT_MARGIN)
	if err != nil {
		return fmt.Errorf("最終建玉情報の取得に失敗しました: %w", err)
	}
	ords, err := gateway.GetOrders(context.Background())
	if err != nil {
		return fmt.Errorf("総発注情報の取得に失敗しました: %w", err)
	}
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
	presenter.PrintPerformanceReport(report)

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

var jstLocation = time.FixedZone("JST", 9*60*60)

// inferPriceRangeGroupsFromCSV はCSV冒頭の価格・板情報から銘柄ごとの呼値グループ（標準 / TOPIX100）を推定します。
// 東証標準の呼値刻みで割り切れない価格（例: 3000円以下の0.1/0.5円刻み、3001-5000円の1円刻み等）が観測された場合は TOPIX100 と判定します。
func inferPriceRangeGroupsFromCSV(csvPath string, enabledAssets map[string]portfolio.SymbolTarget) map[string]symbol.PriceRangeGroup {
	result := make(map[string]symbol.PriceRangeGroup, len(enabledAssets))
	for code, asset := range enabledAssets {
		if asset.PriceRangeGroup != 0 {
			result[code] = asset.PriceRangeGroup
		} else {
			result[code] = symbol.PRICE_RANGE_GROUP_TSE_STANDARD
		}
	}

	file, err := os.Open(csvPath)
	if err != nil {
		return result
	}
	defer file.Close()

	reader := csv.NewReader(file)
	if _, err := reader.Read(); err != nil { // skip header
		return result
	}

	stdSym := symbol.Symbol{PriceRangeGroup: symbol.PRICE_RANGE_GROUP_TSE_STANDARD}
	seenPerSymbol := make(map[string]int, len(enabledAssets))
	const maxSamplesPerSymbol = 200
	const maxRowsToScan = 20000

	checkPrice := func(code string, p float64) {
		if p <= 0 || result[code] == symbol.PRICE_RANGE_GROUP_TSE_TOPIX100 {
			return
		}
		stdTick := stdSym.CalcTickSize(p)
		if stdTick <= 0 {
			return
		}
		rem := math.Mod(p, stdTick)
		if rem > 1e-6 && math.Abs(rem-stdTick) > 1e-6 {
			result[code] = symbol.PRICE_RANGE_GROUP_TSE_TOPIX100
		}
	}

	for row := 0; row < maxRowsToScan; row++ {
		record, err := reader.Read()
		if err != nil {
			break
		}
		if len(record) < 9 {
			continue
		}
		code := record[1]
		asset, ok := enabledAssets[code]
		if !ok || asset.PriceRangeGroup != 0 {
			continue
		}
		if seenPerSymbol[code] >= maxSamplesPerSymbol {
			continue
		}
		seenPerSymbol[code]++

		if p, err := strconv.ParseFloat(record[2], 64); err == nil {
			checkPrice(code, p)
		}
		if askP, err := strconv.ParseFloat(record[5], 64); err == nil {
			checkPrice(code, askP)
		}
		if bidP, err := strconv.ParseFloat(record[7], 64); err == nil {
			checkPrice(code, bidP)
		}
		if len(record) >= 45 && result[code] != symbol.PRICE_RANGE_GROUP_TSE_TOPIX100 {
			for i := 0; i < 9; i++ {
				base := 9 + (i * 4)
				if p, err := strconv.ParseFloat(record[base], 64); err == nil {
					checkPrice(code, p)
				}
				if p, err := strconv.ParseFloat(record[base+2], 64); err == nil {
					checkPrice(code, p)
				}
			}
		}
	}

	return result
}

func runCustomCSVFeeder(csvPath string, tickChan chan<- tick.Tick) error {
	// 🌟 CSVファイル名から日付 (YYYYMMDD) を抽出。デフォルトは実行当日のJST日付
	baseDate := time.Now().In(jstLocation)
	baseName := filepath.Base(csvPath)
	for i := 0; i <= len(baseName)-8; i++ {
		sub := baseName[i : i+8]
		if _, err := strconv.Atoi(sub); err == nil {
			if d, err := time.ParseInLocation("20060102", sub, jstLocation); err == nil {
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

	header, err := reader.Read()
	if err != nil {
		return err
	}

	colIdx := make(map[string]int, len(header))
	for i, col := range header {
		colIdx[strings.ToLower(strings.TrimSpace(col))] = i
	}
	findCol := func(names ...string) int {
		for _, name := range names {
			if idx, ok := colIdx[name]; ok {
				return idx
			}
		}
		return -1
	}

	statusColIdx := findCol("currentpricestatus", "current_price_status")
	changeStatusColIdx := findCol("currentpricechangestatus", "current_price_change_status")
	openingPriceColIdx := findCol("openingprice", "opening_price")
	prevCloseColIdx := findCol("previousclose", "previous_close", "前日終値")
	tradingValueColIdx := findCol("tradingvalue", "trading_value")
	mktSellQtyColIdx := findCol("marketordersellqty", "market_order_sell_qty")
	mktBuyQtyColIdx := findCol("marketorderbuyqty", "market_order_buy_qty")
	overSellQtyColIdx := findCol("oversellqty", "over_sell_qty")
	underBuyQtyColIdx := findCol("underbuyqty", "under_buy_qty")

	parseFloatCol := func(record []string, idx int) float64 {
		if idx < 0 || idx >= len(record) || record[idx] == "" {
			return 0
		}
		v, _ := strconv.ParseFloat(record[idx], 64)
		return v
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
		// 🌟 抽出した日付と時刻をJST固定でマージして完全な time.Time を生成
		tickTime := time.Date(
			baseDate.Year(), baseDate.Month(), baseDate.Day(),
			parsedTime.Hour(), parsedTime.Minute(), parsedTime.Second(), parsedTime.Nanosecond(),
			jstLocation,
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
		fallbackStatusIdx := 9 // デフォルト（旧フォーマット）

		// フル板情報がある場合 (新フォーマット)
		if len(record) >= 46 {
			fallbackStatusIdx = 45
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
		sIdx := statusColIdx
		if sIdx < 0 {
			sIdx = fallbackStatusIdx
		}
		if sIdx >= 0 && len(record) > sIdx {
			if s, err := strconv.Atoi(record[sIdx]); err == nil {
				status = s
			}
		}

		var changeStatus tick.PriceChangeStatus
		if changeStatusColIdx >= 0 && len(record) > changeStatusColIdx {
			changeStatus = tick.PriceChangeStatus(record[changeStatusColIdx])
		}

		t := tick.Tick{
			Symbol:        record[1],
			Price:         price,
			OpeningPrice:  parseFloatCol(record, openingPriceColIdx),
			PreviousClose: parseFloatCol(record, prevCloseColIdx),
			TradingVolume: volume,
			TradingValue:  parseFloatCol(record, tradingValueColIdx),
			VWAP:          vwap,
			BestAsk: tick.FirstQuote{
				Price: askPrice,
				Qty:   askQty,
			},
			BestBid: tick.FirstQuote{
				Price: bidPrice,
				Qty:   bidQty,
			},
			SellBoard:                sellBoard,
			BuyBoard:                 buyBoard,
			CurrentPriceTime:         tickTime,
			CurrentPriceStatus:       tick.PriceStatus(status),
			CurrentPriceChangeStatus: changeStatus,
			MarketOrderSellQty:       parseFloatCol(record, mktSellQtyColIdx),
			MarketOrderBuyQty:        parseFloatCol(record, mktBuyQtyColIdx),
			OverSellQty:              parseFloatCol(record, overSellQtyColIdx),
			UnderBuyQty:              parseFloatCol(record, underBuyQtyColIdx),
		}

		tickChan <- t
	}

	close(tickChan)
	return nil
}


