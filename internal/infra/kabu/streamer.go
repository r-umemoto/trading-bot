package kabu

import (
	"context"
	"fmt"
	"time"
	"trading-bot/internal/domain/market"
)

// KabuMarketAdapter はカブコムの不揃いなAPI仕様を吸収し、統一されたストリームに変換します
type KabuMarketAdapter struct {
	wsURL           string
	client          *KabuClient
	processedOrders map[string]bool // 通知済みの注文IDを記録し、重複検知を防ぐ
}

func NewKabuMarketAdapter(wsURL string, client *KabuClient) *KabuMarketAdapter {
	return &KabuMarketAdapter{
		wsURL:           wsURL,
		client:          client,
		processedOrders: make(map[string]bool),
	}
}

// Start は market.EventStreamer の実装です
func (a *KabuMarketAdapter) Start(ctx context.Context) (<-chan market.Tick, <-chan market.ExecutionReport, error) {
	priceCh := make(chan market.Tick, 100)
	execCh := make(chan market.ExecutionReport, 10)

	// 1. 株価のWebSocketを裏側で起動（既存の WebSocket 処理）
	go a.startWebSocketLoop(ctx, priceCh)

	// 2. 約定のポーリングを裏側で起動（先ほど話していた Watcher 処理）
	go a.startPollingLoop(ctx, execCh)

	// 呼び出し側（Engine）には、美しく整えられた2つのチャネルだけを返す
	return priceCh, execCh, nil
}

func (a *KabuMarketAdapter) startWebSocketLoop(ctx context.Context, tickCh chan market.Tick) {
	// 既存のWebSocketクライアントを起動
	rawCh := make(chan PushMessage)
	wsClient := NewWSClient(a.wsURL)
	go wsClient.Listen(rawCh)

	// 🔄 変換層（アダプター処理）
	go func() {
		defer close(tickCh)
		for {
			select {
			case <-ctx.Done():
				// システム終了時は安全にゴルーチンを抜ける
				return
			case msg := <-rawCh:
				// ★ ここで「カブコム専用データ」を「システム共通データ」に翻訳！
				tickCh <- market.Tick{
					Symbol: msg.Symbol,
					Price:  msg.CurrentPrice,
				}
			}
		}
	}()
}

func (a *KabuMarketAdapter) startPollingLoop(ctx context.Context, execCh chan market.ExecutionReport) {
	ticker := time.NewTicker(3 * time.Second) // 3秒間隔でポーリング
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return // コンテキストキャンセルで安全に終了

		case <-ticker.C:
			// APIから注文一覧を取得 (※KabuClientの実装に合わせてメソッド名は調整してください)
			apiOrders, err := a.client.GetOrders()
			if err != nil {
				// ネットワークエラー等はログだけ出して次のTickを待つ
				fmt.Printf("ポーリングエラー: %v\n", err)
				continue
			}

			for _, apiOrder := range apiOrders {
				// kabusapiの仕様: State == 3 が「処理済（約定）」
				if apiOrder.State == 3 {
					// すでに通知済みの注文IDならスキップ
					if a.processedOrders[apiOrder.ID] {
						continue
					}

					// kabusapiの売買区分(Side)をドメインのActionに変換（1:売, 2:買 の場合）
					action := market.Buy
					if apiOrder.Side == "1" {
						action = market.Sell
					}

					// 約定イベントを生成してチャネルに送信
					execCh <- market.ExecutionReport{
						OrderID: apiOrder.ID,
						Symbol:  apiOrder.Symbol,
						Action:  action,
						Price:   apiOrder.Price,
						Qty:     apiOrder.CumQty,
					}

					// 送信完了として記録
					a.processedOrders[apiOrder.ID] = true
				}
			}
		}
	}
}
