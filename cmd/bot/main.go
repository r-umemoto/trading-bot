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

	"trading-bot/internal/domain/sniper"
	"trading-bot/internal/infra/kabu"
)

func main() {
	fmt.Println("システム起動: 初期化プロセスを開始します。")

	// 1. コンテキスト（OSシグナル）の準備
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 2. インフラ（APIクライアント）の準備
	apiPassword := os.Getenv("KABU_API_PASSWORD")
	client := kabu.NewKabuClient("http://localhost:18080/kabusapi", "")
	if err := client.GetToken(apiPassword); err != nil {
		log.Fatalf("❌ トークン取得エラー: %v", err)
	}
	fmt.Println("✅ APIトークン取得完了")

	// 起動時クリーンアップ
	cleanupInitialPositions(client, apiPassword)

	// 3. アプリケーションの組み立て（DI: 依存性の注入）
	snipers, streamer := buildPortfolio(client, apiPassword)

	// 4. 司令部（Engine）の生成
	engine := NewEngine(streamer, snipers)

	// 時間指定キルスイッチ
	go killSwitch(ctx, stop, client, snipers)

	// 5. 実行！（ブロックされる）
	if err := engine.Run(ctx); err != nil {
		log.Fatalf("❌ システム異常終了: %v", err)
	}
}

// cmd/bot/main.go の killSwitch 関数を修正

// killSwitch は指定時刻に全スナイパーへ撤収命令を出します
func killSwitch(ctx context.Context, cancel context.CancelFunc, client *kabu.KabuClient, snipers []*sniper.Sniper) {
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

// cmd/bot/main.go の下部に追加

// cleanupInitialPositions は起動時に残存している建玉をすべて成行で強制決済します。
// 完全にノーポジションになったことを確認できない場合はエラーを返します。
func cleanupInitialPositions(client *kabu.KabuClient, apiPassword string) error {
	fmt.Println("🧹 起動時のシステム状態チェックを開始します...")

	initialPositions, err := client.GetPositions("2")
	if err != nil {
		return fmt.Errorf("建玉取得エラー: %w", err)
	}

	cleaned := false
	for _, pos := range initialPositions {
		if pos.LeavesQty > 0 {
			qty := int(pos.LeavesQty)
			fmt.Printf("🔥 前回の残存建玉を発見。成行で強制決済します: %s %d株\n", pos.SymbolName, qty)

			req := kabu.OrderRequest{
				Password:       apiPassword,
				Symbol:         pos.Symbol,
				Exchange:       1,
				SecurityType:   1,
				Side:           "1", // 売
				Qty:            qty,
				FrontOrderType: 10, // 成行
				Price:          0,
			}
			if _, err := client.SendOrder(req); err != nil {
				return fmt.Errorf("強制決済の発注エラー (%s): %w", pos.SymbolName, err)
			}
			cleaned = true
		}
	}

	if cleaned {
		fmt.Println("⏳ クリーンアップの約定処理を待機中 (3秒)...")
		time.Sleep(3 * time.Second)

		// 最終確認：本当に全部消えたか？
		finalPositions, err := client.GetPositions("2")
		if err != nil {
			return fmt.Errorf("最終確認での建玉取得エラー: %w", err)
		}
		for _, pos := range finalPositions {
			if pos.LeavesQty > 0 {
				return fmt.Errorf("🚨 クリーンアップ後も建玉が残っています (%s: %f株)。手動で確認してください", pos.SymbolName, pos.LeavesQty)
			}
		}
		fmt.Println("✅ クリーンアップ完了。システムはノーポジションから開始します。")
	} else {
		fmt.Println("✅ 残存建玉はありません。クリーンな状態で起動します。")
	}

	return nil
}
