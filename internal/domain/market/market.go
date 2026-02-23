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

type Action string

const (
	Buy  Action = "BUY"
	Sell Action = "SELL"
)

// ExecutionReport は市場で発生した約定の事実を表します
type ExecutionReport struct {
	OrderID string  // 紐づく注文ID
	Symbol  string  // 銘柄
	Action  Action  // 買いか売りか
	Price   float64 // 実際の約定単価
	Qty     uint32  // 実際に約定した数量
}
