// internal/domain/market/analyzer.go
package market

import (
	"sync"
)

// MarketState は、特定銘柄の「今の市場環境」の生データを保持するものです
type MarketState struct {
	Symbol         string
	LatestTick     Tick      // 最新のTickデータ（出来高等を含む）
	Recent10Prices []float64 // 直近10回の価格履歴

	// B: 急落検知（将来拡張）
	// DropVelocity float64

	// C: 板情報（将来拡張）
	// BuyBoardPressure float64
}

// DataPool は生の市場データを受け取り、戦略が必要なデータを集約・提供するインタフェースです
type DataPool interface {
	PushTick(tick Tick)
	GetState(symbol string) MarketState
}

// DefaultDataPool は DataPool インターフェースの標準実装です
type DefaultDataPool struct {
	states map[string]MarketState
	mu     sync.RWMutex
}

func NewDefaultDataPool() *DefaultDataPool {
	return &DefaultDataPool{
		states: make(map[string]MarketState),
	}
}

// PushTick は新しいTickデータを受け取り、内部のデータプールを更新します
func (a *DefaultDataPool) PushTick(tick Tick) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// 1. 公開用状態の取得・初期化
	state, exists := a.states[tick.Symbol]
	if !exists {
		state = MarketState{Symbol: tick.Symbol}
	}

	// 最新のTickを保持
	state.LatestTick = tick

	// 履歴を保持
	state.Recent10Prices = append(state.Recent10Prices, tick.Price)
	if len(state.Recent10Prices) > 10 {
		state.Recent10Prices = state.Recent10Prices[len(state.Recent10Prices)-10:]
	}

	// 状態を保存
	a.states[tick.Symbol] = state
}

// GetState は指定銘柄の最新の市場状態を返します
func (a *DefaultDataPool) GetState(symbol string) MarketState {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.states[symbol] // 存在しない場合はゼロ値の構造体が返ります
}
