// internal/domain/market/analyzer.go
package market

import (
	"sync"

	"github.com/r-umemoto/trading-bot/pkg/domain/market/calculator"
)

// DefaultAnalyzer は Analyzer インターフェースの標準実装です
type DefaultAnalyzer struct {
	states     map[string]MarketState
	calcStates map[string]*calculator.SigmaCalculator // calculatorパッケージのものに置き換え
	mu         sync.RWMutex                           // 複数ゴルーチンからのアクセスに備えてロックを用意
}

func NewDefaultAnalyzer() *DefaultAnalyzer {
	return &DefaultAnalyzer{
		states:     make(map[string]MarketState),
		calcStates: make(map[string]*calculator.SigmaCalculator),
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

	// 2. 内部計算用状態(SigmaCalculator)の取得・初期化
	calc, calcExists := a.calcStates[tick.Symbol]
	if !calcExists {
		// 初回起動直後の1Tick目は、現在までの総出来高をセットして初期化するだけ
		calc = calculator.NewSigmaCalculator(tick.TradingVolume)
		a.calcStates[tick.Symbol] = calc

		// 状態を記録して、計算自体はスキップして終了
		state.CurrentPrice = tick.Price
		state.VWAP = tick.VWAP
		a.states[tick.Symbol] = state
		return
	}

	// 3. 2回目以降のTick処理：計算をSigmaCalculatorに完全委譲
	state.CurrentPrice = tick.Price
	state.VWAP = tick.VWAP
	state.Sigma = calc.UpdateAndGetSigma(tick.TradingVolume, tick.Price)

	// 更新した状態を保存
	a.states[tick.Symbol] = state
}

// GetState は指定銘柄の最新の市場状態（加工済み指標）を返します
func (a *DefaultAnalyzer) GetState(symbol string) MarketState {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.states[symbol] // 存在しない場合はゼロ値の構造体が返ります
}
