package market

import "context"

// Tick はシステム共通の価格データ（カブコムの仕様を一切知らない純粋なデータ）
type Tick struct {
	Symbol string
	Price  float64
}

// PriceStreamer は株価を配信するサービスの共通規格
type PriceStreamer interface {
	// コンテキストを受け取り、Tickが延々と流れてくる専用の管（チャネル）を返す
	Subscribe(ctx context.Context, symbols []string) (<-chan Tick, error)
}
