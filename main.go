package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"
)

// main.go の killSwitch関数を上書き

// キルスイッチ（指定時刻に未約定の注文をすべてキャンセルする）
func killSwitch(ctx context.Context, cancel context.CancelFunc, client *KabuClient) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			// テスト用に、今から1〜2分後の時間を設定して実験してみてください
			// 例: 現在 15:30 なら t.Hour() == 15 && t.Minute() >= 32
			if t.Hour() == 23 && t.Minute() >= 41 {
				fmt.Println("\n⏰【緊急指令】14:50に到達。キルスイッチを作動します！")

				// 1. 現在出ている注文一覧を取得
				orders, err := client.GetOrders()
				if err != nil {
					log.Printf("キルスイッチ: 注文照会エラー: %v\n", err)
				} else {
					// 2. 未約定（State: 3）の注文をすべてキャンセル
					for _, order := range orders {
						if order.State == 3 {
							fmt.Printf("🛑 未約定の注文(ID: %s)をキャンセルします...\n", order.ID)

							req := CancelRequest{
								OrderID:  order.ID,
								Password: "your_test_password",
							}
							_, err := client.CancelOrder(req)
							if err != nil {
								log.Printf("キャンセル失敗 (ID: %s): %v\n", order.ID, err)
							} else {
								fmt.Printf("✅ キャンセル成功 (ID: %s)\n", order.ID)
							}
						}
					}
				}

				// すべての処理に終了を通知してプログラムを停止
				cancel()
				return
			}
		}
	}
}

func main() {
	fmt.Println("スナイパーボット、起動シーケンス開始。")

	baseURL := "http://localhost:18080/kabusapi"
	kabuClient := NewKabuClient(baseURL, "")

	// システム全体の状態を管理するコンテキストを作成
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. APIパスワードを環境変数から取得（※テスト時は直接書いてもOKです）
	apiPassword := os.Getenv("KABU_API_PASSWORD")
	if apiPassword == "" {
		// 開発中はここで直接指定してもOKです
		apiPassword = "your_test_password"
		// log.Fatal("環境変数 KABU_API_PASSWORD が設定されていません")
	}

	// 3. 【最重要】トークンの取得とセット
	fmt.Println("🔑 APIトークンを取得中...")
	if err := kabuClient.GetToken(apiPassword); err != nil {
		log.Fatalf("トークン取得エラー: %v\n(kabuステーションが起動し、APIが有効になっているか確認してください)", err)
	}
	fmt.Println("✅ トークン取得成功！APIの準備が整いました。")

	fmt.Println("口座のポジション情報を取得中...")

	// "2" は信用取引の建玉のみを取得する指定
	positions, err := kabuClient.GetPositions("2")
	if err != nil {
		log.Fatalf("ポジションの取得に失敗しました: %v", err)
	}

	if len(positions) == 0 {
		fmt.Println("現在保有している建玉はありません。エントリー用の別プログラムを待機します。")
		return // ポジションが無ければスナイパーは出番なし
	}

	// ★ 1. 監視対象を管理するためのマップを作成 ★
	// キー: 銘柄コード(文字列), バリュー: 目標価格と発注済みフラグを持つ構造体
	type TargetInfo struct {
		TargetPrice float64
		HasSold     bool
	}
	targets := make(map[string]*TargetInfo)
	// 取得したポジション情報を使って監視準備
	for _, pos := range positions {
		if pos.LeavesQty > 0 { // 決済可能な株がある場合のみ
			tp := pos.Price * 1.002 // 0.2%上を計算

			targets[pos.Symbol] = &TargetInfo{
				TargetPrice: tp,
				HasSold:     false,
			}

			fmt.Printf("🎯 監視対象マップに登録: %s(%s) | 建値: %.1f円 -> 利確目標: %.1f円\n",
				pos.SymbolName, pos.Symbol, pos.Price, tp)
		}
	}

	// 価格データを受け取るための「パイプ（Channel）」を作成
	// バッファを100くらい持たせて、処理落ちを防ぎます
	priceChannel := make(chan PushMessage, 100)

	// WebSocketクライアントの生成（kabuステーションのデフォルトWSポート）
	wsClient := NewWSClient("ws://localhost:18080/kabusapi/websocket")

	// WebSocketの受信ループを別プロセス（Goroutine）で起動
	go wsClient.Listen(priceChannel)

	// キルスイッチを別プロセス（ゴルーチン）で起動
	go killSwitch(ctx, cancel, kabuClient)

	// メインの取引ループ（脳）
	fmt.Println("市場の監視を開始します...")

	for {
		select {
		case <-ctx.Done():
			// キルスイッチ実行
			fmt.Println("システムを安全にシャットダウンしました。お疲れ様でした。")
			return

		// WebSocketから新しい価格データがパイプを通って届いた瞬間、ここが発火する
		case msg := <-priceChannel:
			// ★ 2. 届いた価格データが、監視対象マップ（targets）に入っているかチェック ★
			target, exists := targets[msg.Symbol]
			if !exists {
				// 監視対象外の銘柄のデータは無視して次を待つ
				continue
			}

			fmt.Printf("[リアルタイム受信] %s: %.1f円 (目標: %.1f円)\n", msg.SymbolName, msg.CurrentPrice, target.TargetPrice)

			// ★ 利益確定ロジック ★
			// まだ決済しておらず、かつ現在値が目標価格（4008円）以上になったら！
			if !target.HasSold && msg.CurrentPrice >= target.TargetPrice {
				fmt.Printf("\n🔥【利確シグナル発動！】%sが目標の%.1f円に到達！（現在値: %.1f円）\n",
					msg.SymbolName, target.TargetPrice, msg.CurrentPrice)

				// 2重発注防止のためにフラグを立てる
				target.HasSold = true

				// 利確のための「信用返済（売り・成行）」リクエストを作成
				orderReq := OrderRequest{
					Password:        "your_order_password", // 発注パスワード
					Symbol:          msg.Symbol,            // WebSocketで降ってきた銘柄（9433等）
					Exchange:        1,                     // 東証
					SecurityType:    1,                     // 株式
					Side:            "1",                   // 1: 売り
					CashMargin:      3,                     // 3: 信用返済
					MarginTradeType: 3,                     // 3: 一般信用デイトレ（1日信用）
					AccountType:     4,                     // 4: 特定口座
					Qty:             100,                   // 100株
					Price:           0,                     // 0: 成行決済（確実に逃げるため）
					ExpireDay:       0,                     // 当日限り
					FrontOrderType:  10,                    // 10: 成行
				}

				// 狙撃（発注）実行！
				response, err := kabuClient.SendOrder(orderReq)
				if err != nil {
					log.Printf("【致命的エラー】利確注文の送信に失敗しました: %v\n", err)
					// ここでLINE通知などを飛ばす処理を入れると完璧
				} else {
					fmt.Printf("🎯 利益確定の注文完了！ 受付ID: %s\n", response.OrderId)
				}

				// ※テスト用：利確したら今日は店じまい（キルスイッチと同じく終了させる）
				// cancel()
			}
		}
	}
}
