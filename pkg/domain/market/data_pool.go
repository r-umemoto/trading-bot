// internal/domain/market/analyzer.go
package market

import (
	"sync"

	"github.com/r-umemoto/trading-bot/pkg/domain/market/calculator"
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
	GetSigma(symbol string) float64
	GetVWAP(symbol string) float64
}

// DefaultDataPool は DataPool インターフェースの標準実装です
type DefaultDataPool struct {
	states     map[string]MarketState
	calcStates map[string]*calculator.SigmaCalculator
	mu         sync.RWMutex
}

func NewDefaultDataPool() *DefaultDataPool {
	return &DefaultDataPool{
		states:     make(map[string]MarketState),
		calcStates: make(map[string]*calculator.SigmaCalculator),
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

	// 指標計算用の累積状態を更新（計算自体は行わない）
	calc, calcExists := a.calcStates[tick.Symbol]
	if !calcExists {
		calc = calculator.NewSigmaCalculator(tick.TradingVolume)
		a.calcStates[tick.Symbol] = calc
	} else {
		calc.Update(tick.TradingVolume, tick.Price)
	}
}

// GetState は指定銘柄の最新の市場状態を返します
func (a *DefaultDataPool) GetState(symbol string) MarketState {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.states[symbol] // 存在しない場合はゼロ値の構造体が返ります
}

// GetSigma は指定銘柄の標準偏差（σ）をオンデマンドで評価・計算して返します
func (a *DefaultDataPool) GetSigma(symbol string) float64 {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if calc, exists := a.calcStates[symbol]; exists {
		return calc.GetSigma()
	}
	return 0
}

// GetVWAP は指定銘柄のVWAPをオンデマンドで評価・計算して返します
func (a *DefaultDataPool) GetVWAP(symbol string) float64 {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if calc, exists := a.calcStates[symbol]; exists {
		state := a.states[symbol]
		return calc.GetVWAP(state.LatestTick.Price)
	}
	return 0
}
