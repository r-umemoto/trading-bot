package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/service"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
	"github.com/r-umemoto/trading-bot/pkg/infra/backtest"
	"github.com/r-umemoto/trading-bot/pkg/portfolio"
)

func main() {
	var csvPath string
	flag.StringVar(&csvPath, "csv", "./data/all_20260409.csv", "バックテスト用CSVファイルのパス")
	var portfolioPath string
	flag.StringVar(&portfolioPath, "portfolio", "./configs/portfolio.json", "ポートフォリオJSONファイルのパス")
	flag.Parse()

	fmt.Printf("Private 戦略のバックテストを開始します... (データ: %s)\n", csvPath)

	// 2. 監視銘柄（Sniper）のセットアップ
	targets, err := portfolio.LoadFromJSON(portfolioPath)
	if err != nil {
		log.Fatalf("ポートフォリオの読み込みに失敗しました: %v", err)
	}
	watchList := portfolio.BuildWatchList(targets)

	// 3. バックテスト用インフラ（Mock Gateway）と DataPool の準備
	gateway := backtest.NewBacktestGateway()
	dataPool := market.NewDefaultDataPool()
	tickCh, execCh, _ := gateway.Start(context.Background())

	var snipers []*sniper.Sniper
	for _, sym := range watchList {
		factory, err := strategy.GetFactory(sym.StrategyName)
		if err != nil {
			log.Fatalf("戦略 '%s' が見つかりません: %v", sym.StrategyName, err)
		}
		s := sniper.NewSniper(sym.Symbol, factory.NewStrategy(sym.Symbol, dataPool), sym.Exchange)
		snipers = append(snipers, s)
	}

	// PositionCleaner の起動 (Gatewayに依存するため)
	_ = service.NewPositionCleaner(snipers, gateway)

	// 5. Feederの準備 (時間を正しくパースするためのカスタム実装)
	csvTickChan := make(chan market.Tick, 1000)

	// Feederを別ゴルーチンで起動し、CSVの読み込みを開始
	go func() {
		if err := runCustomCSVFeeder(csvPath, csvTickChan); err != nil {
			log.Fatalf("Feeder実行エラー: %v", err)
		}
	}()

	tickCount := 0

	// 6. メインループ: CSVから送られてくるTickを限界速度でGatewayへ流し込む
	for tick := range csvTickChan {
		tickCount++

		// ここでバックテストGatewayにTickを流し、同時に注文の約定判定をさせる
		gateway.ProcessTick(tick)

		// ゲートウェイの中で発生した約定（execCh）を全て吸い出して同期処理する
		for len(execCh) > 0 {
			report := <-execCh
			for _, s := range snipers {
				if s.Symbol == report.Symbol {
					s.OnExecution(report)
				}
			}
		}

		// ゲートウェイから転送されてきたTickを受け取る
		t := <-tickCh

		// TradeUseCase をバイパスし、直接 DataPool 更新と Sniper 評価を行う（完全に同期的）
		dataPool.PushTick(t)
		for _, s := range snipers {
			if s.Symbol == t.Symbol {
				orderPtr, req := s.Tick(dataPool)
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

		// 進捗確認用のログ（10万件ごとに出力）
		if tickCount%100000 == 0 {
			fmt.Printf("%d件のTickを処理完了...\n", tickCount)
		}
	}

	// 7. 取引終了後のクリーンアップ: 最後のTickで生成された「成行決済注文」などを、最終価格で強制作約定させる
	ordersToMopUp, _ := gateway.GetOrders(context.Background())
	fmt.Printf("クリーンアップ開始: 未約定の可能性がある注文数 %d件\n", len(ordersToMopUp))
	for _, o := range ordersToMopUp {
		if !o.IsCompleted() {
			fmt.Printf("未約定注文を発見: %s %s %s\n", o.ID, o.Symbol, o.Action)
			state := dataPool.GetState(o.Symbol)
			if !state.LatestTick.CurrentPriceTime.IsZero() {
				// 最終価格をエミュレートしてGatewayに流し込む
				gateway.ProcessTick(state.LatestTick)
				// その結果発生した約定も同期処理する
				for len(execCh) > 0 {
					report := <-execCh
					for _, s := range snipers {
						if s.Symbol == report.Symbol {
							s.OnExecution(report)
						}
					}
				}
				// 飛ばしたTickを捨てる
				<-tickCh

				fmt.Printf("最終Tickでプロセス実行: %s (Price: %f)\n", o.Symbol, state.LatestTick.Price)
			}
		}
	}

	// pending channel flush (実行レポートの処理を少し待つ)
	time.Sleep(100 * time.Millisecond)

	fmt.Printf("バックテスト完了: 総処理Tick数 %d件\n", tickCount)

	// 結果の出力
	positions, _ := gateway.GetPositions(context.Background(), market.PRODICT_CASH)
	orders, _ := gateway.GetOrders(context.Background())
	fmt.Println("=== バックテスト結果 ===")
	for _, p := range positions {
		fmt.Printf("最終建玉: 銘柄 %s, 数量 %.f\n", p.Symbol, p.LeavesQty)
	}
	fmt.Printf("総発注数: %d件\n", len(orders))

	// 簡単な損益計算（約定履歴から、決済済み建玉のみを計算する）
	var realizedPnL float64
	// 銘柄ごとに保有単価と保有数量をトラッキング
	type PositionState struct {
		Qty      float64
		AvgPrice float64
	}
	posMap := make(map[string]*PositionState)

	for _, o := range orders {
		if !o.IsCompleted() {
			continue
		}

		state, exists := posMap[o.Symbol]
		if !exists {
			state = &PositionState{}
			posMap[o.Symbol] = state
		}

		if o.Action == market.ACTION_BUY {
			// 平均取得単価の更新
			totalCost := (state.Qty * state.AvgPrice) + (o.FilledQty() * o.AveragePrice())
			state.Qty += o.FilledQty()
			state.AvgPrice = totalCost / state.Qty
			// fmt.Printf("BUY: %s %.0f株 @ %.2f (Avg: %.2f)\n", o.Symbol, o.FilledQty(), o.AveragePrice(), state.AvgPrice)
		} else if o.Action == market.ACTION_SELL {
			// 実現損益の計算: (売値 - 平均取得単価) * 売却数量
			sellQty := o.FilledQty()
			if state.Qty < sellQty {
				// 空売り等は考慮外とする簡易計算
				sellQty = state.Qty
			}

			tradePnL := (o.AveragePrice() - state.AvgPrice) * sellQty
			realizedPnL += tradePnL
			// fmt.Printf("SELL: %s %.0f株 @ %.2f (PnL: %+.0f)\n", o.Symbol, sellQty, o.AveragePrice(), tradePnL)

			state.Qty -= sellQty
			if state.Qty <= 0 {
				state.Qty = 0
				state.AvgPrice = 0
			}
		}
	}

	// 含み損益の計算
	var unrealizedPnL float64
	for symbol, state := range posMap {
		if state.Qty > 0 {
			marketState := dataPool.GetState(symbol)
			if !marketState.LatestTick.CurrentPriceTime.IsZero() {
				latestPrice := marketState.LatestTick.Price
				unrealized := (latestPrice - state.AvgPrice) * state.Qty
				unrealizedPnL += unrealized
				// fmt.Printf("UNREALIZED: %s %.0f株 @ %.2f (Avg: %.2f, PnL: %+.0f)\n", symbol, state.Qty, latestPrice, state.AvgPrice, unrealized)
			}
		}
	}

	fmt.Printf("実現損益: %+.0f 円\n", realizedPnL)
	fmt.Printf("含み損益: %+.0f 円\n", unrealizedPnL)
	fmt.Printf("合計損益: %+.0f 円\n", realizedPnL+unrealizedPnL)
}

func runCustomCSVFeeder(csvPath string, tickChan chan<- market.Tick) error {
	file, err := os.Open(csvPath)
	if err != nil {
		return err
	}
	defer file.Close()

	reader := csv.NewReader(file)

	// ヘッダー行をスキップ
	if _, err := reader.Read(); err != nil {
		return err
	}

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break // ファイルの末尾に到達
		}
		if err != nil {
			return err
		}

		// CSVの各カラムを元の型にパース
		// フォーマット: "Time", "Symbol", "Price", "TradingVolume", "VWAP"
		// 時刻を正しくパースしてセットすることで、条件クラス(UpTrend)で正しく動くようにする
		parsedTime, _ := time.Parse("15:04:05.000", record[0])
		price, _ := strconv.ParseFloat(record[2], 64)
		volume, _ := strconv.ParseFloat(record[3], 64)
		vwap, _ := strconv.ParseFloat(record[4], 64)

		tick := market.Tick{
			Symbol:           record[1],
			Price:            price,
			TradingVolume:    volume,
			VWAP:             vwap,
			CurrentPriceTime: parsedTime, // ここが重要
		}

		// バックテストエンジン（またはAnalyzer）に向けてTickを送信
		tickChan <- tick
	}

	// 全データの送信が完了したらチャネルを閉じる
	close(tickChan)
	return nil
}
