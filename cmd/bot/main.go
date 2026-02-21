// cmd/bot/main.go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"trading-bot/internal/engine"
	"trading-bot/internal/kabu"
)

func main() {
	fmt.Println("システム起動: 初期化プロセスを開始します。")

	// 1. 全体を安全に停止するためのコンテキスト管理
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 2. APIクライアントの準備とトークン取得
	apiPassword := os.Getenv("KABU_API_PASSWORD")
	if apiPassword == "" {
		apiPassword = "dummy_password"
	}
	client := kabu.NewKabuClient("http://localhost:18080/kabusapi", "")

	if err := client.GetToken(apiPassword); err != nil {
		log.Fatalf("トークン取得エラー: %v", err)
	}
	fmt.Println("✅ APIトークン取得完了")

	// 3. 建玉の取得と戦略の配置（並列テスト）
	positions, err := client.GetPositions("2")
	if err != nil {
		log.Fatalf("ポジション取得エラー: %v", err)
	}

	var snipers []*engine.Sniper
	for _, pos := range positions {
		if pos.LeavesQty > 0 {
			qty := int(pos.LeavesQty)

			// 戦略A: 0.2% での利確監視
			strategyA := engine.NewFixedRateStrategy(pos.Symbol, pos.Price, 0.002, qty)
			snipers = append(snipers, engine.NewSniper(pos.Symbol, strategyA, client))

			// 戦略B: 0.3% での利確監視（並列でテスト）
			strategyB := engine.NewFixedRateStrategy(pos.Symbol, pos.Price, 0.003, qty)
			snipers = append(snipers, engine.NewSniper(pos.Symbol, strategyB, client))

			fmt.Printf("🎯 監視登録完了: %s 建値: %.1f円 -> [戦略A: 0.2%%], [戦略B: 0.3%%]\n", pos.SymbolName, pos.Price)
		}
	}

	// 4. WebSocketからの価格受信チャネル
	priceCh := make(chan kabu.PushMessage)

	// ここで goroutine を使って websocket.go の Listen処理などを起動します
	// WebSocketクライアントの生成（kabuステーションのデフォルトWSポート）
	wsClient := kabu.NewWSClient("ws://localhost:18080/kabusapi/websocket")

	// WebSocketの受信ループを別プロセス（Goroutine）で起動
	go wsClient.Listen(priceCh)

	// 5. キルスイッチの起動
	go killSwitch(ctx, cancel, client, snipers)

	// OSからの終了シグナル（Ctrl+C）を受け取る準備
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	fmt.Println("🚀 市場の監視を開始します...")

	// 6. メインループ（Pub/Sub モデルによる価格の分配）
	for {
		select {
		case <-ctx.Done():
			fmt.Println("システムを安全にシャットダウンします。")
			return

		case <-sigCh:
			fmt.Println("\n中断シグナルを受信しました。終了処理に入ります。")
			cancel()

		case msg := <-priceCh:
			fmt.Printf("🎯 価格データ受信: 建値: %.1f円 \n", msg.CurrentPrice)
			// 受信した価格データを、登録されているすべての戦略に分配する
			for _, s := range snipers {
				if s.Symbol == msg.Symbol {
					s.OnPriceUpdate(msg.CurrentPrice)
				}
			}
		}
	}
}

// cmd/bot/main.go の killSwitch 関数を修正

// killSwitch は指定時刻に全スナイパーへ撤収命令を出します
func killSwitch(ctx context.Context, cancel context.CancelFunc, client *kabu.KabuClient, snipers []*engine.Sniper) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	apiPassword := "dummy_password" // 本番は環境変数から

	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			if (t.Hour() == 14 && t.Minute() >= 50) || t.Hour() >= 15 {
				fmt.Println("\n⏰【キルスイッチ作動】14:50到達。全スナイパーに撤収を命じます！")

				// 1. 全スナイパーに一斉に撤収命令を出す（並列実行も可能ですが今回は直列で確実に行います）
				for _, sniper := range snipers {
					sniper.ForceExit(apiPassword)
				}

				// 2. 取引所の約定処理を待機
				fmt.Println("⏳ 全スナイパーの撤収完了。取引所の約定データ反映を待機中 (3秒)...")
				time.Sleep(3 * time.Second)

				// 3. 最終ポジション確認（死力確認）
				fmt.Println("🔍 最終ポジション確認を実行します...")
				finalPositions, err := client.GetPositions("2")
				if err == nil {
					remainingCount := 0
					for _, pos := range finalPositions {
						if pos.LeavesQty > 0 {
							remainingCount++
							fmt.Printf("⚠️ 警告: 建玉が残っています！ 銘柄: %s, 残数量: %f\n", pos.SymbolName, pos.LeavesQty)
						}
					}

					if remainingCount == 0 {
						fmt.Println("✅ 【完全勝利】すべての建玉の決済が確認されました。ノーポジションです。")
						cancel() // 成功した時だけシャットダウン！
						return
					} else {
						// 失敗時は cancel() も return もしない！
						fmt.Printf("🚨 【緊急事態】未決済の建玉が %d 件残っています！\n", remainingCount)
						fmt.Println("🔄 30秒後に強制決済プロセスをリトライします...")
						time.Sleep(30 * time.Second) // 👈 証券会社へのDDoSを防ぐためのインターバル
					}
				} else {
					fmt.Printf("❌ 最終確認での建玉取得エラー: %v\n", err)
					fmt.Println("🔄 30秒後に強制決済プロセスをリトライします...")
					time.Sleep(30 * time.Second)
				}
			}
		}
	}
}
