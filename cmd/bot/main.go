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

	"trading-bot/internal/kabu"
	"trading-bot/internal/sniper"
	"trading-bot/internal/sniper/strategy"
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

	// ---------------------------------------------------
	// 起動時の残存建玉クリーンアップ
	// ---------------------------------------------------
	if err := cleanupInitialPositions(client, apiPassword); err != nil {
		log.Fatalf("❌ 起動時クリーンアップ失敗: %v\n", err)
	}
	// ---------------------------------------------------

	// 3. 監視対象銘柄の定義（監視リスト）
	type target struct {
		Symbol string
		Qty    uint32
	}
	watchList := []target{
		{
			Symbol: "9433",
			Qty:    100,
		},
	} // KDDIをターゲットに設定

	var snipers []*sniper.Sniper
	for _, target := range watchList {
		// 戦略の組み立て（コンポジット）
		buyStrategy := strategy.NewLimitBuy(3990.0, int(target.Qty))
		sellStrategy := strategy.NewFixedRate(3990.0, 0.002, int(target.Qty))
		// ①と②を包括的戦略（1往復トレード）として束ねる
		masterStrategy := strategy.NewRoundTrip(buyStrategy, sellStrategy)
		// 2. 🚨 本来の戦略をキルスイッチで包み込む（ラップする）
		safeLogic := strategy.NewKillSwitch(masterStrategy, 100)

		// スナイパーに包括的戦略を渡して配備
		snipers = append(snipers, sniper.NewSniper(target.Symbol, safeLogic, client))

		fmt.Printf("🎯 新規監視リスト登録: %s -> [3990円で買 -> +0.2%%で売]の包括戦略をセット完了\n", target.Symbol)
	}

	// 4. WebSocketからの価格受信チャネル
	priceCh := make(chan kabu.PushMessage)
	wsClient := kabu.NewWSClient("ws://localhost:18080/kabusapi/websocket")
	go wsClient.Listen(priceCh)

	// ---------------------------------------------------
	// 🎯 究極のコンテキスト管理（OSシグナルと連動）
	// Ctrl+C が押されると、自動的に ctx が Done になります
	// ---------------------------------------------------
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 5. キルスイッチの起動
	go killSwitch(ctx, stop, client, snipers)

	// OSからの終了シグナル（Ctrl+C）を受け取る準備
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	fmt.Println("🚀 市場の監視を開始します...")

	// 6. メインループ（Pub/Sub モデルによる価格の分配）
Loop:
	for {
		select {
		case <-ctx.Done():
			fmt.Println("システムを安全にシャットダウンします。")
			break Loop

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

	// ===================================================
	// 🎯 ここから下は「死に際の処理（Graceful Shutdown）」
	// ===================================================
	fmt.Println("\n🚨 全スナイパーに緊急撤退命令を出します...")
	for _, s := range snipers {
		// ここでスナイパー内部の OnPriceUpdate(0.0) が発火し、成行売りが飛ぶ！
		s.EmergencyExit()
	}

	// 最後に少しだけAPI通信の完了を待ってあげる
	fmt.Println("⏳ 撤退注文の通信完了を待機中 (3秒)...")
	time.Sleep(3 * time.Second)

	fmt.Println("システムを安全にシャットダウンします。")
	// ここで main 関数が終わりに到達し、自然にプロセスが落ちる
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
			if (t.Hour() == 14 && t.Minute() >= 50) || t.Hour() >= 315 {
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
