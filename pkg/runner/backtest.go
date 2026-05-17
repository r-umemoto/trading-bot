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
	flag.Parse()

	execModel := backtest.ExecutionModel(execModelStr)

	fmt.Printf("戦略のバックテストを開始します... (データ: %s, 約定モデル: %s)\n", csvPath, execModel)

	// 2. 監視銘柄（Sniper）のセットアップ
	targets, err := portfolio.LoadFromJSON(portfolioPath)
	if err != nil {
		return fmt.Errorf("ポートフォリオの読み込みに失敗しました: %w", err)
	}

	// 3. バックテスト用インフラ（Mock Gateway）と DataPool の準備
	gateway := backtest.NewBacktestGateway(execModel)
	dataPool := tick.NewDefaultDataPool()
	tickCh, orderReportCh, _ := gateway.Start(context.Background())

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

	var snipers []*sniper.Sniper
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

		s := sniper.NewSniper(sym.Detail, st, policy, sym.Exchange, analysisLogger)
		snipers = append(snipers, s)
	}

	// PositionCleaner の起動 (Gatewayに依存するため)
	_ = service.NewPositionCleaner(snipers, gateway)

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
			for _, s := range snipers {
				// 🌟 修正: SyncOrders から戻る IFD 発注要求を処理する
				ifdOrderPtr, _ := s.SyncOrders(report)
				if ifdOrderPtr != nil {
					fmt.Printf("🚀 [%s] SyncOrders経由でIFD決済注文を送信します: %s %.2f株\n", s.Detail.Code, ifdOrderPtr.Action, ifdOrderPtr.OrderQty)
					updatedOrder, err := gateway.SendOrder(context.Background(), *ifdOrderPtr)
					if err != nil {
						s.FailSendingOrder(ifdOrderPtr)
					} else {
						*ifdOrderPtr = updatedOrder
					}
				}
			}
		}

		t := <-tickCh

		dataPool.PushTick(t)
		for _, s := range snipers {
			if s.Detail.Code == t.Symbol {
				orderPtr, cancelOrderID := s.Tick(dataPool)

				if cancelOrderID != "" {
					fmt.Printf("🛑 [Backtest] 自動キャンセルを実行: %s\n", cancelOrderID)
					_ = gateway.CancelOrder(context.Background(), cancelOrderID)
					continue
				}

				if orderPtr != nil {
					updatedOrder, err := gateway.SendOrder(context.Background(), *orderPtr)
					if err != nil {
						s.FailSendingOrder(orderPtr)
					} else {
						*orderPtr = updatedOrder
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
					for _, s := range snipers {
						ifdOrderPtr, _ := s.SyncOrders(report)
						if ifdOrderPtr != nil {
							updatedOrder, err := gateway.SendOrder(context.Background(), *ifdOrderPtr)
							if err != nil {
								s.FailSendingOrder(ifdOrderPtr)
							} else {
								*ifdOrderPtr = updatedOrder
							}
						}
					}
				}
				<-tickCh
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
	uc := usecase.NewTradeUseCase(snipers, gateway, dataPool)
	uc.PrintPerformanceReport(false)

	return nil
}

func runCustomCSVFeeder(csvPath string, tickChan chan<- tick.Tick) error {
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
			CurrentPriceTime:   parsedTime,
			CurrentPriceStatus: tick.PriceStatus(status),
		}

		tickChan <- tick
	}

	close(tickChan)
	return nil
}
