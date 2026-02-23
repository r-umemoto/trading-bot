// internal/infra/kabu/watcher.go
package kabu

import (
	"time"
	"trading-bot/internal/domain/market"
)

// ExecutionWatcher はAPIを監視し、約定イベントを発行します
type ExecutionWatcher struct {
	client *KabuClient
}

func NewExecutionWatcher(client *KabuClient) *ExecutionWatcher {
	return &ExecutionWatcher{client: client}
}

// Start は監視を開始し、約定通知が流れてくるチャネルを返します
func (w *ExecutionWatcher) Start(interval time.Duration) <-chan market.ExecutionReport {
	ch := make(chan market.ExecutionReport, 10) // バッファ付きチャネル

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		// ※実際には「前回どこまで処理したか」を記憶する仕組みが必要です
		for range ticker.C {
			apiOrders, err := w.client.GetOrders()
			if err != nil {
				continue
			}

			for _, apiOrder := range apiOrders {
				if apiOrder.State == 3 /* 処理済(約定) */ {
					// 誰の注文かは気にせず、事実だけをチャネルに流す
					ch <- market.ExecutionReport{
						OrderID: apiOrder.ID,
						Symbol:  apiOrder.Symbol,
						Action:  market.Buy, // APIの値をマッピング
						// Price:   apiOrder.ExecutionPrice,
						// Qty:     apiOrder.ExecutionQty,
					}
				}
			}
		}
	}()

	return ch // 呼び出し側はこのチャネルを監視する
}
