// internal/domain/market/analyzer.go
package market

import "sync"

// DefaultAnalyzer は Analyzer インターフェースの標準実装です
type DefaultAnalyzer struct {
	states map[string]MarketState
	mu     sync.RWMutex // 複数ゴルーチンからのアクセスに備えてロックを用意
}

func NewDefaultAnalyzer() *DefaultAnalyzer {
	return &DefaultAnalyzer{
		states: make(map[string]MarketState),
	}
}

// UpdateTick は新しいTickデータを受け取り、内部の指標を再計算します
func (a *DefaultAnalyzer) UpdateTick(tick Tick) {
	a.mu.Lock()
	defer a.mu.Unlock()

	state, exists := a.states[tick.Symbol]
	if !exists {
		state = MarketState{Symbol: tick.Symbol}
	}

	state.CurrentPrice = tick.Price
	state.VWAP = tick.VWAP

	a.states[tick.Symbol] = state
}

// GetState は指定銘柄の最新の市場状態（加工済み指標）を返します
func (a *DefaultAnalyzer) GetState(symbol string) MarketState {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.states[symbol] // 存在しない場合はゼロ値の構造体が返ります
}
