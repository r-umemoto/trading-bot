package tick

import (
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const orderRTTWindowSize = 30

type DataPool interface {
	PushTick(tick Tick)
	GetState(symbol string) MarketState

	// 新規汎用指標システム Indicatorをシングルトンで管理する
	GetOrCreateIndicator(symbol, id string, factory func() Indicator) Indicator

	// 発注API RTT（応答時間）の記録と直近サンプル取得
	RecordOrderRTT(d time.Duration)
	OrderRTTSamples(dst []float64) int
}

// DefaultDataPool は DataPool インターフェースの標準実装です
type DefaultDataPool struct {
	symbols        sync.Map // Key: string (symbol), Value: *symbolData
	feederProvider HistoricalFeederProvider

	rttMu      sync.RWMutex
	rttSamples [orderRTTWindowSize]float64
	rttIndex   int
	rttCount   int
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

func NewDefaultDataPool(provider HistoricalFeederProvider) *DefaultDataPool {
	return &DefaultDataPool{
		feederProvider: provider,
	}
}

// RecordOrderRTT は発注APIのRTT（応答時間）を固定長リングバッファへ記録します。
func (a *DefaultDataPool) RecordOrderRTT(d time.Duration) {
	if d < 0 {
		d = 0
	}
	ms := float64(d.Microseconds()) / 1000.0

	a.rttMu.Lock()
	a.rttSamples[a.rttIndex] = ms
	a.rttIndex = (a.rttIndex + 1) % orderRTTWindowSize
	if a.rttCount < orderRTTWindowSize {
		a.rttCount++
	}
	a.rttMu.Unlock()
}

// OrderRTTSamples は保持している直近の発注API RTTサンプル(ms)を時系列順（古→新）で dst へコピーし、コピー件数を返します。
func (a *DefaultDataPool) OrderRTTSamples(dst []float64) int {
	a.rttMu.RLock()
	defer a.rttMu.RUnlock()

	n := a.rttCount
	if n > len(dst) {
		n = len(dst)
	}
	if n == 0 {
		return 0
	}
	// rttIndex は次に書き込む位置（最新サンプルの1つ先）のため、直近 n 件の開始位置をリングバッファ上で逆算する
	start := (a.rttIndex - n + orderRTTWindowSize) % orderRTTWindowSize
	for i := 0; i < n; i++ {
		dst[i] = a.rttSamples[(start+i)%orderRTTWindowSize]
	}
	return n
}

func (a *DefaultDataPool) getOrCreateSymbolData(symbol string) *symbolData {
	if val, ok := a.symbols.Load(symbol); ok {
		return val.(*symbolData)
	}

	data := newSymbolData(symbol)
	if val, loaded := a.symbols.LoadOrStore(symbol, data); loaded {
		return val.(*symbolData)
	}
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
	if factory == nil {
		data.mu.Unlock()
		return nil
	}

	// ファクトリー関数の中でさらに GetOrCreateIndicator が呼ばれた場合（複合インジケーターなど）に
	// デッドロックするのを防ぐため、一旦ロックを解除してからファクトリーを実行する（Double-Checked Locking）
	data.mu.Unlock()
	newInd := factory()
	if newInd == nil {
		return nil
	}

	// 🌟 追加：ヒストリカルフェッチを必要とする指標の場合、自動初期化を安全にロックの外で走らせる
	if fetcher, ok := newInd.(FetcherIndicator); ok && a.feederProvider != nil {
		feeder := a.feederProvider.GetFeeder(symbol)
		if feeder != nil {
			if err := fetcher.FetchAndInitialize(feeder); err != nil {
				slog.Error("Failed to initialize indicator with historical data",
					slog.String("symbol", symbol),
					slog.String("indicator", id),
					slog.Any("error", err),
				)
			}
		}
	}

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
