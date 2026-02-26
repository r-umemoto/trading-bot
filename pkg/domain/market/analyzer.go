// internal/domain/market/analyzer.go
package market

import (
	"math"
	"sync"
)

// DefaultAnalyzer は Analyzer インターフェースの標準実装です
type DefaultAnalyzer struct {
	states     map[string]MarketState
	calcStates map[string]*vwapCalcState // 銘柄ごとの計算用状態を保持
	mu         sync.RWMutex              // 複数ゴルーチンからのアクセスに備えてロックを用意
}

func NewDefaultAnalyzer() *DefaultAnalyzer {
	return &DefaultAnalyzer{
		states:     make(map[string]MarketState),
		calcStates: make(map[string]*vwapCalcState),
	}
}

// UpdateTick は新しいTickデータを受け取り、内部の指標を再計算します
func (a *DefaultAnalyzer) UpdateTick(tick Tick) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// 1. 公開用状態の取得・初期化
	state, exists := a.states[tick.Symbol]
	if !exists {
		state = MarketState{Symbol: tick.Symbol}
	}

	// 2. 内部計算用状態の取得・初期化
	calc, calcExists := a.calcStates[tick.Symbol]
	if !calcExists {
		calc = &vwapCalcState{}
		a.calcStates[tick.Symbol] = calc
	}

	state.CurrentPrice = tick.Price
	state.VWAP = tick.VWAP

	// 3. 出来高の差分計算の前に、初回起動（または再起動）の判定を入れる
	if calc.prevVolume == 0 {
		// 起動直後の1Tick目は、現在までの総出来高を記録するだけ（計算はしない）
		calc.prevVolume = tick.TradingVolume
		return
	}

	// 3. 出来高（tick.Volumeは当日の累積出来高を想定）の差分からTick出来高を算出
	tickVolume := tick.TradingVolume - calc.prevVolume
	if tickVolume > 0 {
		// 約定が発生した場合のみ Σ(P^2 * V) を更新
		calc.sumPrice2Volume += (tick.Price * tick.Price) * tickVolume
		calc.prevVolume = tick.TradingVolume
	}

	// 4. σ（標準偏差）の計算: 分散 = (Σ(P^2 * V) / 総出来高) - VWAP^2
	if tick.TradingVolume > 0 {
		variance := (calc.sumPrice2Volume / tick.TradingVolume) - (tick.VWAP * tick.VWAP)
		if variance > 0 {
			state.Sigma = math.Sqrt(variance)
		} else {
			state.Sigma = 0
		}
	}

	// 更新した状態を保存
	a.states[tick.Symbol] = state
}

// GetState は指定銘柄の最新の市場状態（加工済み指標）を返します
func (a *DefaultAnalyzer) GetState(symbol string) MarketState {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.states[symbol] // 存在しない場合はゼロ値の構造体が返ります
}

// σ計算用に保持する内部状態
type vwapCalcState struct {
	prevVolume      float64
	sumPrice2Volume float64
}
