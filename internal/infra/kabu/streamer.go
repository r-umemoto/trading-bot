package kabu

import (
	"context"
	"fmt"
	"time"
	"trading-bot/internal/domain/market"
)

// KabuMarketAdapter はカブコムの不揃いなAPI仕様を吸収し、統一されたストリームに変換します
type KabuMarketAdapter struct {
	wsURL               string
	gateway             *KabuOrderBroker
	processedExecutions map[string]bool // 通知済みの注文IDを記録し、重複検知を防ぐ
}

func NewKabuMarketAdapter(wsURL string, gateway *KabuOrderBroker) *KabuMarketAdapter {
	return &KabuMarketAdapter{
		wsURL:               wsURL,
		gateway:             gateway,
		processedExecutions: make(map[string]bool),
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
					VWAP:   msg.VWAP,
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
			return

		case <-ticker.C:
			orders, err := a.gateway.GetOrders(ctx)
			if err != nil {
				fmt.Printf("ポーリングエラー: %v\n", err)
				continue
			}

			// 1. 注文(Order)のループ
			for _, order := range orders {

				// 2. さらに明細(Details)のループを回す！
				for _, detail := range order.Executions {

					// 約定IDが空の明細（単なる「受付済」などのステータス履歴）はスキップ
					if detail.ID == "" {
						continue
					}

					// 🌟 注文IDではなく「約定ID」で通知済みかを判定する
					if a.processedExecutions[detail.ID] {
						continue
					}

					// 約定イベントを生成してチャネルに送信
					execCh <- market.ExecutionReport{
						OrderID:     order.ID,
						ExecutionID: detail.ID, // レポートにも約定IDを持たせる
						Symbol:      order.Symbol,
						Action:      order.Action,
						Price:       detail.Price, // 👈 Details側の「実際の約定単価」
						Qty:         detail.Qty,   // 👈 Details側の「実際の約定数量」
					}

					// 🌟 処理完了として「約定ID」を記録する
					a.processedExecutions[detail.ID] = true
				}
			}
		}
	}
}
