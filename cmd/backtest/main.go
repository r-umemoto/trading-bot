// cmd/backtest/main.go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/service"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
	"github.com/r-umemoto/trading-bot/pkg/infra/backtest"
	"github.com/r-umemoto/trading-bot/pkg/infra/feed"
	"github.com/r-umemoto/trading-bot/pkg/usecase"
)

func main() {
	// 読み込むCSVファイルのパス（収集したデータ）
	csvPath := "./data/all_20260311.csv" // プロジェクトルートから実行した場合のパス

	// 1. 戦略のセットアップ
	// 実行したい戦略名と対象銘柄を指定します
	strategyName := "simple" // ← ここを実際の戦略名に書き換えてください
	factory, err := strategy.Get(strategyName)
	if err != nil {
		log.Fatalf("戦略が見つかりません: %v", err)
	}

	targetSymbol := "7201" // CSV内の銘柄と一致させる必要があります
	s := sniper.NewSniper(targetSymbol, factory.NewStrategy(), market.EXCHANGE_TOSHO_PLUS)
	snipers := []*sniper.Sniper{s}

	// 2. バックテスト用インフラ（Mock Gateway）と DataPool の準備
	gateway := backtest.NewBacktestGateway()
	dataPool := market.NewDefaultDataPool()
	tickCh, execCh, _ := gateway.Start(context.Background())

	// 3. ユースケース（ワーカー）の構築と起動
	tradeUC := usecase.NewTradeUseCase(snipers, gateway, dataPool)
	_ = service.NewPositionCleaner(snipers, gateway)

	// 4. Feederの準備
	feeder := feed.NewCSVTickFeeder(csvPath)
	csvTickChan := make(chan market.Tick, 1000)

	// Feederを別ゴルーチンで起動し、CSVの読み込みを開始
	go func() {
		if err := feeder.Run(csvTickChan); err != nil {
			log.Fatalf("Feeder実行エラー: %v", err)
		}
	}()

	fmt.Println("バックテストを開始します...")
	tickCount := 0

	// 銘柄のワーカーを裏で回しておく（BacktestGatewayが発火するexecChなどを処理）
	tradeUC.StartWorkers(context.Background())

	// executionレポートをルーティングするGoroutine
	go func() {
		for report := range execCh {
			tradeUC.HandleExecution(report)
		}
	}()

	// 5. メインループ: CSVから送られてくるTickを限界速度でGatewayへ流し込む
	for tick := range csvTickChan {
		tickCount++

		// ここでバックテストGatewayにTickを流し、同時に注文の約定判定をさせる
		gateway.ProcessTick(tick)

		// ゲートウェイから転送されてきたTickをユースケースに流す
		t := <-tickCh
		tradeUC.HandleTick(context.Background(), t)

		// 進捗確認用のログ（10万件ごとに出力）
		if tickCount%100000 == 0 {
			fmt.Printf("%d件のTickを処理完了...\n", tickCount)
		}
	}

	fmt.Printf("バックテスト完了: 総処理Tick数 %d件\n", tickCount)

	// 結果の出力
	positions, _ := gateway.GetPositions(context.Background(), market.PRODICT_CASH)
	orders, _ := gateway.GetOrders(context.Background())
	fmt.Println("=== バックテスト結果 ===")
	for _, p := range positions {
		fmt.Printf("最終建玉: 銘柄 %s, 数量 %.f\n", p.Symbol, p.LeavesQty)
	}
	fmt.Printf("総発注数: %d件\n", len(orders))
}
