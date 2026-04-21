// internal/domain/market/analyzer.go
package market

import (
	"fmt"
	"sync"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/market/calculator"
)

// Tick 価格変動で発生するイベント
type Tick struct {
	Symbol           string
	Price            float64
	VWAP             float64
	TradingVolume    float64   // 売買高
	CurrentPriceTime time.Time // 現値時刻
}

func NewTick(symbol string, price float64, vwap float64, tradingVolume float64, currentPriceTime time.Time) Tick {
	return Tick{
		Symbol:           symbol,
		Price:            price,
		VWAP:             vwap,
		TradingVolume:    tradingVolume,
		CurrentPriceTime: currentPriceTime,
	}
}

// MarketState は、特定銘柄の「今の市場環境」の生データを保持するものです
type MarketState struct {
	Symbol     string
	LatestTick Tick // 最新のTickデータ（出来高等を含む）

	// B: 急落検知（将来拡張）
	// DropVelocity float64

	// C: 板情報（将来拡張）
	// BuyBoardPressure float64
}

// FiveMinSummary は5分間の出来高とVWAPのサマリ結果を保持します
type FiveMinSummary struct {
	StartTime     time.Time
	EndTime       time.Time
	TradingVolume float64
	VWAP          float64
}

// DataPool は生の市場データを受け取り、戦略が必要なデータを集約・提供するインタフェースです
type DataPool interface {
	PushTick(tick Tick)
	GetState(symbol string) MarketState
	GetSigma(symbol string) float64
	GetVWAP(symbol string) float64
	GetFiveMinSummaries(symbol string) []calculator.FiveMinSummary
	GetCurrentFiveMinVWAP(symbol string) float64

	// 新規汎用指標システム
	GetOrCreateIndicator(symbol, id string, factory func() Indicator) Indicator
}

// DefaultDataPool は DataPool インターフェースの標準実装です
type DefaultDataPool struct {
	states          map[string]MarketState
	calcStates      map[string]*calculator.SigmaCalculator
	fiveMinCalcs    map[string]*calculator.FiveMinCalculator
	indicators      map[string]map[string]Indicator // symbol -> id -> Indicator
	indicatorsOrder map[string][]Indicator          // symbol -> 登録順のIndicatorリスト（決定論的なUpdate順序を保証）
	mu              sync.RWMutex
}

func NewDefaultDataPool() *DefaultDataPool {
	return &DefaultDataPool{
		states:          make(map[string]MarketState),
		calcStates:      make(map[string]*calculator.SigmaCalculator),
		fiveMinCalcs:    make(map[string]*calculator.FiveMinCalculator),
		indicators:      make(map[string]map[string]Indicator),
		indicatorsOrder: make(map[string][]Indicator),
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

	// 5分足集計の更新
	fiveMinCalc, fiveMinExists := a.fiveMinCalcs[tick.Symbol]
	if !fiveMinExists {
		fiveMinCalc = calculator.NewFiveMinCalculator()
		a.fiveMinCalcs[tick.Symbol] = fiveMinCalc
	}
	fiveMinCalc.Update(tick.Price, tick.TradingVolume, tick.CurrentPriceTime)

	// 登録されている順序で汎用指標をすべて更新する（順序不定による1Tick遅延バグを防止）
	for _, ind := range a.indicatorsOrder[tick.Symbol] {
		ind.Update(tick)
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

// GetFiveMinSummaries は指定銘柄の5分ごとのサマリ配列を返します
func (a *DefaultDataPool) GetFiveMinSummaries(symbol string) []calculator.FiveMinSummary {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if calc, exists := a.fiveMinCalcs[symbol]; exists {
		return calc.GetSummaries()
	}
	return []calculator.FiveMinSummary{}
}

// GetCurrentFiveMinVWAP は現在蓄積中の5分枠のリアルタイムVWAPを返します
func (a *DefaultDataPool) GetCurrentFiveMinVWAP(symbol string) float64 {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if calc, exists := a.fiveMinCalcs[symbol]; exists {
		return calc.GetCurrentVWAP()
	}
	return 0
}

// GetOrCreateIndicator は指定した銘柄とIDの指標を取得し、無ければ生成して登録します
func (a *DefaultDataPool) GetOrCreateIndicator(symbol, id string, factory func() Indicator) Indicator {
	a.mu.Lock()
	defer a.mu.Unlock()

	if _, exists := a.indicators[symbol]; !exists {
		a.indicators[symbol] = make(map[string]Indicator)
	}

	if ind, exists := a.indicators[symbol][id]; exists {
		return ind
	}

	newInd := factory()
	a.indicators[symbol][id] = newInd
	
	// 依存関係に基づいてトポロジカルソートを実行し、決定論的な更新順序を保証する
	a.rebuildOrder(symbol)
	
	return newInd
}

// rebuildOrder はシンボル内の全インジケーターの依存グラフを解析し、
// 依存先が必ず先に更新されるように indicatorsOrder を再構築します（トポロジカルソート）
func (a *DefaultDataPool) rebuildOrder(symbol string) {
	indicators := a.indicators[symbol]

	var order []Indicator
	visited := make(map[string]bool)
	inProgress := make(map[string]bool)

	var visit func(ind Indicator) error
	visit = func(ind Indicator) error {
		if inProgress[ind.ID()] {
			return fmt.Errorf("circular dependency detected involving %s", ind.ID())
		}
		if visited[ind.ID()] {
			return nil
		}
		inProgress[ind.ID()] = true

		for _, dep := range ind.Dependencies() {
			if dep != nil {
				if err := visit(dep); err != nil {
					return err
				}
			}
		}

		inProgress[ind.ID()] = false
		visited[ind.ID()] = true
		order = append(order, ind)
		return nil
	}

	for _, ind := range indicators {
		if !visited[ind.ID()] {
			if err := visit(ind); err != nil {
				// 循環参照などの致命的な設計エラーは、沈黙させずにPanicさせて開発者に直させる
				panic(fmt.Sprintf("Fatal: Indicator TopoSort failed for %s: %v", symbol, err))
			}
		}
	}

	a.indicatorsOrder[symbol] = order
}
