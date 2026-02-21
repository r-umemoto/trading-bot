package kabu

import (
	"context"
	"trading-bot/internal/market"
)

// KabuStreamer は market.PriceStreamer インターフェースを満たすカブコム専用アダプター
type KabuStreamer struct {
	wsURL string
}

func NewKabuStreamer(wsURL string) *KabuStreamer {
	return &KabuStreamer{wsURL: wsURL}
}

// Subscribe はカブコムのWebSocketを起動し、共通のTickに変換して流し続ける
func (s *KabuStreamer) Subscribe(ctx context.Context, symbols []string) (<-chan market.Tick, error) {
	tickCh := make(chan market.Tick)

	// 既存のWebSocketクライアントを起動
	rawCh := make(chan PushMessage)
	wsClient := NewWSClient(s.wsURL)
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

	return tickCh, nil
}
