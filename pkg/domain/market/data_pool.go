// internal/domain/market/analyzer.go
package market

import (
	"fmt"
	"sync"
)

type DataPool interface {
	PushTick(tick Tick)
	GetState(symbol string) MarketState

	// 新規汎用指標システム Indicatorをシングルトンで管理する
	GetOrCreateIndicator(symbol, id string, factory func() Indicator) Indicator
}

// DefaultDataPool は DataPool インターフェースの標準実装です
type DefaultDataPool struct {
	symbols map[string]*symbolData
	mu      sync.RWMutex // symbols マップ自体の操作（新しい銘柄の登録時）を保護
}

// symbolData は銘柄ごとのデータを保持し、個別にロックを制御します
type symbolData struct {
	state           MarketState
	indicators      map[string]Indicator // id -> Indicator
	indicatorsOrder []Indicator          // 登録順のIndicatorリスト
	mu              sync.RWMutex
}

func newSymbolData(symbol string) *symbolData {
	return &symbolData{
		state:      MarketState{Symbol: symbol},
		indicators: make(map[string]Indicator),
	}
}

func NewDefaultDataPool() *DefaultDataPool {
	return &DefaultDataPool{
		symbols: make(map[string]*symbolData),
	}
}

func (a *DefaultDataPool) getOrCreateSymbolData(symbol string) *symbolData {
	a.mu.RLock()
	data, exists := a.symbols[symbol]
	a.mu.RUnlock()

	if exists {
		return data
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	// Double check
	if data, exists = a.symbols[symbol]; exists {
		return data
	}

	data = newSymbolData(symbol)
	a.symbols[symbol] = data
	return data
}

// PushTick は新しいTickデータを受け取り、内部のデータプールを更新します
func (a *DefaultDataPool) PushTick(tick Tick) {
	data := a.getOrCreateSymbolData(tick.Symbol)
	data.mu.Lock()
	defer data.mu.Unlock()

	// 最新のTickを保持
	data.state.LatestTick = tick

	// 登録されている順序で汎用指標をすべて更新する
	for _, ind := range data.indicatorsOrder {
		ind.Update(tick)
	}
}

// GetState は指定銘柄の最新の市場状態を返します
func (a *DefaultDataPool) GetState(symbol string) MarketState {
	data := a.getOrCreateSymbolData(symbol)
	data.mu.RLock()
	defer data.mu.RUnlock()
	return data.state
}

// GetOrCreateIndicator は指定した銘柄とIDの指標を取得し、無ければ生成して登録します
func (a *DefaultDataPool) GetOrCreateIndicator(symbol, id string, factory func() Indicator) Indicator {
	data := a.getOrCreateSymbolData(symbol)

	data.mu.Lock()
	if ind, exists := data.indicators[id]; exists {
		data.mu.Unlock()
		return ind
	}

	// ファクトリー関数の中でさらに GetOrCreateIndicator が呼ばれた場合（複合インジケーターなど）に
	// デッドロックするのを防ぐため、一旦ロックを解除してからファクトリーを実行する（Double-Checked Locking）
	data.mu.Unlock()
	newInd := factory()
	data.mu.Lock()
	defer data.mu.Unlock()

	// ロック解除中に別のゴルーチンが作成していた場合の再チェック
	if ind, exists := data.indicators[id]; exists {
		return ind
	}

	data.indicators[id] = newInd

	// 依存関係に基づいてトポロジカルソートを実行し、決定論的な更新順序を保証する
	data.rebuildOrder()

	return newInd
}

// rebuildOrder はシンボル内の全インジケーターの依存グラフを解析し、
// 依存先が必ず先に更新されるように indicatorsOrder を再構築します（トポロジカルソート）
func (s *symbolData) rebuildOrder() {
	indicators := s.indicators

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
				panic(fmt.Sprintf("Fatal: Indicator TopoSort failed for %s: %v", s.state.Symbol, err))
			}
		}
	}

	s.indicatorsOrder = order
}
