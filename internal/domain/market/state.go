// internal/domain/market/state.go
package market

// MarketState は、特定銘柄の「今の市場環境」を数値化したものです
type MarketState struct {
	Symbol       string
	CurrentPrice float64

	// A: テクニカル指標
	ShortMA float64
	LongMA  float64
	VWAP    float64

	// B: 急落検知（将来拡張）
	// DropVelocity float64

	// C: 板情報（将来拡張）
	// BuyBoardPressure float64
}

// Analyzer は生の市場データを受け取り、加工済みの MarketState を提供する規格です
type Analyzer interface {
	UpdateTick(tick Tick)
	GetState(symbol string) MarketState
}
