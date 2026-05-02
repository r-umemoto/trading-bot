package runner

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/service"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
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
	flag.Parse()

	fmt.Printf("戦略のバックテストを開始します... (データ: %s)\n", csvPath)

	// 2. 監視銘柄（Sniper）のセットアップ
	targets, err := portfolio.LoadFromJSON(portfolioPath)
	if err != nil {
		return fmt.Errorf("ポートフォリオの読み込みに失敗しました: %w", err)
	}

	// 3. バックテスト用インフラ（Mock Gateway）と DataPool の準備
	gateway := backtest.NewBacktestGateway()
	dataPool := market.NewDefaultDataPool()
	tickCh, orderReportCh, _ := gateway.Start(context.Background())

	// 4. 監視リストの構築 (Gatewayを使用して情報を埋める)
	watchList, err := portfolio.BuildWatchList(context.Background(), gateway, targets)
	if err != nil {
		return err
	}

	var snipers []*sniper.Sniper
	for _, sym := range watchList {
		factory, err := strategy.GetFactory(sym.StrategyName)
		if err != nil {
			return fmt.Errorf("戦略 '%s' が見つかりません: %w", sym.StrategyName, err)
		}
		s := sniper.NewSniper(sym.Detail, factory.NewStrategy(sym.Detail.Symbol, dataPool), sym.Exchange)
		snipers = append(snipers, s)
	}

	// PositionCleaner の起動 (Gatewayに依存するため)
	_ = service.NewPositionCleaner(snipers, gateway)

	// 5. Feederの準備
	csvTickChan := make(chan market.Tick, 1000)

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
				s.SyncOrders(report.Orders)
			}
		}

		t := <-tickCh

		dataPool.PushTick(t)
		for _, s := range snipers {
			if s.Detail.Symbol == t.Symbol {
				orderPtr, req, cancelOrderID := s.Tick(dataPool)

				if cancelOrderID != "" {
					_ = gateway.CancelOrder(context.Background(), cancelOrderID)
					continue
				}

				if req != nil {
					orderID, err := gateway.SendOrder(context.Background(), *req)
					if err != nil {
						s.FailSendingOrder(orderPtr)
					} else {
						s.ConfirmOrder(orderPtr, orderID)
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
	fmt.Printf("クリーンアップ開始: 未約定の可能性がある注文数 %d件\n", len(ordersToMopUp))
	for _, o := range ordersToMopUp {
		if !o.IsCompleted() {
			state := dataPool.GetState(o.Symbol)
			if !state.LatestTick.CurrentPriceTime.IsZero() {
				gateway.ProcessTick(state.LatestTick)
				for len(orderReportCh) > 0 {
					report := <-orderReportCh
					for _, s := range snipers {
						s.SyncOrders(report.Orders)
					}
				}
				<-tickCh
			}
		}
	}

	time.Sleep(100 * time.Millisecond)

	fmt.Printf("バックテスト完了: 総処理Tick数 %d件\n", tickCount)

	// 結果の出力
	positions, _ := gateway.GetPositions(context.Background(), market.PRODICT_CASH)
	orders, _ := gateway.GetOrders(context.Background())
	fmt.Println("\n=============================================")
	fmt.Println("             バックテスト結果")
	fmt.Println("=============================================")
	for _, p := range positions {
		fmt.Printf("最終建玉: 銘柄 %s, 数量 %.f\n", p.Symbol, p.LeavesQty)
	}
	fmt.Printf("総発注数: %d件\n", len(orders))

	// 結果の出力
	uc := usecase.NewTradeUseCase(snipers, gateway, dataPool)
	uc.PrintPerformanceReport(false)

	return nil
}

func runCustomCSVFeeder(csvPath string, tickChan chan<- market.Tick) error {
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

		status := 1
		if len(record) > 9 {
			if s, err := strconv.Atoi(record[9]); err == nil {
				status = s
			}
		}

		tick := market.Tick{
			Symbol:             record[1],
			Price:              price,
			TradingVolume:      volume,
			VWAP:               vwap,
			CurrentPriceTime:   parsedTime,
			CurrentPriceStatus: status,
		}

		tickChan <- tick
	}

	close(tickChan)
	return nil
}
