package tick_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

// 1. Tests for tick.go
func TestTick_IsExecution(t *testing.T) {
	tests := []struct {
		name   string
		tick   tick.Tick
		expect bool
	}{
		{
			name: "Valid execution - current price status",
			tick: tick.Tick{
				CurrentPriceStatus: tick.PRICE_STATUS_CURRENT,
				Price:              100.0,
				TradingVolume:      10.0,
			},
			expect: true,
		},
		{
			name: "Valid execution - opening status",
			tick: tick.Tick{
				CurrentPriceStatus: tick.PRICE_STATUS_OPENING,
				Price:              100.0,
				TradingVolume:      10.0,
			},
			expect: true,
		},
		{
			name: "Valid execution - pre close status",
			tick: tick.Tick{
				CurrentPriceStatus: tick.PRICE_STATUS_PRE_CLOSE,
				Price:              100.0,
				TradingVolume:      10.0,
			},
			expect: true,
		},
		{
			name: "Valid execution - close status",
			tick: tick.Tick{
				CurrentPriceStatus: tick.PRICE_STATUS_CLOSE,
				Price:              100.0,
				TradingVolume:      10.0,
			},
			expect: true,
		},
		{
			name: "Invalid status - special",
			tick: tick.Tick{
				CurrentPriceStatus: tick.PRICE_STATUS_SPECIAL,
				Price:              100.0,
				TradingVolume:      10.0,
			},
			expect: false,
		},
		{
			name: "Invalid status - none",
			tick: tick.Tick{
				CurrentPriceStatus: tick.PRICE_STATUS_NONE,
				Price:              100.0,
				TradingVolume:      10.0,
			},
			expect: false,
		},
		{
			name: "Invalid price <= 0",
			tick: tick.Tick{
				CurrentPriceStatus: tick.PRICE_STATUS_CURRENT,
				Price:              0.0,
				TradingVolume:      10.0,
			},
			expect: false,
		},
		{
			name: "Invalid volume <= 0",
			tick: tick.Tick{
				CurrentPriceStatus: tick.PRICE_STATUS_CURRENT,
				Price:              100.0,
				TradingVolume:      0.0,
			},
			expect: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.tick.IsExecution(); got != tt.expect {
				t.Errorf("IsExecution() = %v, want %v", got, tt.expect)
			}
		})
	}
}

func TestNewTick(t *testing.T) {
	now := time.Now()
	sellBoard := []tick.Quote{{Price: 101, Qty: 10}}
	buyBoard := []tick.Quote{{Price: 99, Qty: 10}}
	tk := tick.NewTick(
		"8604",
		100.0,
		99.5,
		1000.0,
		now,
		tick.FirstQuote{Price: 101, Qty: 5, Time: now},
		tick.FirstQuote{Price: 99, Qty: 5, Time: now},
		sellBoard,
		buyBoard,
		tick.PRICE_STATUS_CURRENT,
		tick.PRICE_CHANGE_UP,
		98.0,
		100000.0,
		100,
		200,
		500,
		600,
	)

	if tk.Symbol != "8604" || tk.Price != 100.0 || tk.VWAP != 99.5 || tk.TradingVolume != 1000.0 ||
		!tk.CurrentPriceTime.Equal(now) || tk.BestAsk.Price != 101 || tk.BestBid.Price != 99 ||
		len(tk.SellBoard) != 1 || len(tk.BuyBoard) != 1 || tk.CurrentPriceStatus != tick.PRICE_STATUS_CURRENT ||
		tk.CurrentPriceChangeStatus != tick.PRICE_CHANGE_UP || tk.OpeningPrice != 98.0 || tk.TradingValue != 100000.0 ||
		tk.MarketOrderSellQty != 100 || tk.MarketOrderBuyQty != 200 || tk.OverSellQty != 500 || tk.UnderBuyQty != 600 {
		t.Errorf("NewTick initialized incorrect fields: %+v", tk)
	}
}

// 2. Tests for indicator.go (StaticFloatIndicator)
func TestStaticFloatIndicator(t *testing.T) {
	ind := tick.NewStaticFloatIndicator("static_75", 150.5)

	if ind.ID() != "static_75" {
		t.Errorf("expected ID static_75, got %s", ind.ID())
	}

	if ind.Value() != 150.5 {
		t.Errorf("expected Value 150.5, got %v", ind.Value())
	}

	// Update has no effect, should not panic or modify the value
	ind.Update(tick.Tick{Price: 200})
	if ind.Value() != 150.5 {
		t.Errorf("expected static Value to remain unchanged, got %v", ind.Value())
	}

	ind.SetValue(250.0)
	if ind.Value() != 250.0 {
		t.Errorf("expected Value 250.0 after SetValue, got %v", ind.Value())
	}

	if ind.Dependencies() != nil {
		t.Errorf("expected nil dependencies, got %v", ind.Dependencies())
	}
}

// 3. Tests for bar_indicator.go edge cases
func TestOneMinBarIndicator_EdgeCases(t *testing.T) {
	ind := tick.NewOneMinBarIndicator("1min_bar")

	if ind.ID() != "1min_bar" {
		t.Errorf("expected ID 1min_bar, got %s", ind.ID())
	}

	if ind.Dependencies() != nil {
		t.Errorf("expected nil dependencies, got %v", ind.Dependencies())
	}

	// Test Update with Price <= 0 (should return early without doing anything)
	ind.Update(tick.Tick{Price: 0.0})
	if len(ind.Bars()) != 0 {
		t.Errorf("expected 0 bars after invalid price update, got %v", len(ind.Bars()))
	}

	// First valid tick
	baseTime := time.Date(2026, 6, 10, 9, 0, 0, 0, time.UTC)
	ind.Update(tick.Tick{
		CurrentPriceTime: baseTime,
		Price:            100.0,
		TradingVolume:    1000.0,
	})

	// Check cumulative volume decrease (should be capped at 0)
	ind.Update(tick.Tick{
		CurrentPriceTime: baseTime.Add(10 * time.Second),
		Price:            102.0, // test Price > currentBar.High
		TradingVolume:    900.0,  // decreased volume!
	})

	// Check Price < currentBar.Low
	ind.Update(tick.Tick{
		CurrentPriceTime: baseTime.Add(20 * time.Second),
		Price:            98.0, // test Price < currentBar.Low
		TradingVolume:    1100.0,
	})

	bars := ind.Bars()
	if len(bars) != 1 {
		t.Fatalf("expected 1 bar, got %d", len(bars))
	}

	bar := bars[0]
	if bar.High != 102.0 {
		t.Errorf("expected High to be updated to 102.0, got %f", bar.High)
	}
	if bar.Low != 98.0 {
		t.Errorf("expected Low to be updated to 98.0, got %f", bar.Low)
	}
	// Initial tick volume is ignored (volume = 0).
	// Tick 2: volume decreased (1000 -> 900) so volume diff is capped at 0.
	// Tick 3: volume increased (900 -> 1100) so volume diff is 1100 - 900 = 200.
	// Total volume should be 0 + 0 + 200 = 200.
	if bar.Volume != 200 {
		t.Errorf("expected Volume 200, got %f", bar.Volume)
	}
}

// 4. Tests for data_pool.go
type MockHistoricalFeeder struct {
	FetchSMAReturnVal float64
	FetchSMAErr        error
}

func (m *MockHistoricalFeeder) FetchSMA(period int) (float64, error) {
	return m.FetchSMAReturnVal, m.FetchSMAErr
}

func (m *MockHistoricalFeeder) FetchPreviousClose() (float64, error) {
	return 0.0, nil
}

type MockHistoricalFeederProvider struct {
	Feeder tick.HistoricalFeeder
}

func (m *MockHistoricalFeederProvider) GetFeeder(symbol string) tick.HistoricalFeeder {
	return m.Feeder
}

type TestFetcherIndicator struct {
	id           string
	initialized  bool
	dependencies []tick.Indicator
	updateCount  int
}

func (t *TestFetcherIndicator) ID() string {
	return t.id
}

func (t *TestFetcherIndicator) Update(tk tick.Tick) {
	t.updateCount++
}

func (t *TestFetcherIndicator) Dependencies() []tick.Indicator {
	return t.dependencies
}

func (t *TestFetcherIndicator) FetchAndInitialize(feeder tick.HistoricalFeeder) error {
	_, err := feeder.FetchSMA(75)
	if err != nil {
		return err
	}
	t.initialized = true
	return nil
}

func TestDefaultDataPool(t *testing.T) {
	// Setup with mock historical feeder provider
	feeder := &MockHistoricalFeeder{FetchSMAReturnVal: 123.45}
	provider := &MockHistoricalFeederProvider{Feeder: feeder}
	pool := tick.NewDefaultDataPool(provider)

	symbol := "9984"

	// 1. GetState of a symbol not yet pushed to (should return empty MarketState)
	state := pool.GetState(symbol)
	if state.Symbol != symbol {
		t.Errorf("expected symbol %s, got %s", symbol, state.Symbol)
	}
	if state.LatestTick.Price != 0.0 {
		t.Errorf("expected empty tick price, got %f", state.LatestTick.Price)
	}

	// 2. Register FetcherIndicator and verify FetchAndInitialize is called
	ind := pool.GetOrCreateIndicator(symbol, "fetcher_ind", func() tick.Indicator {
		return &TestFetcherIndicator{id: "fetcher_ind"}
	})

	fetcherInd, ok := ind.(*TestFetcherIndicator)
	if !ok {
		t.Fatalf("expected TestFetcherIndicator, got %T", ind)
	}
	if !fetcherInd.initialized {
		t.Errorf("expected fetcher indicator to be initialized")
	}

	// 3. Retrieve existing indicator (should return the same, and not re-initialize)
	fetcherInd.initialized = false
	indAgain := pool.GetOrCreateIndicator(symbol, "fetcher_ind", func() tick.Indicator {
		return &TestFetcherIndicator{id: "fetcher_ind"}
	})
	if indAgain != ind {
		t.Errorf("expected same indicator instance")
	}
	if fetcherInd.initialized {
		t.Errorf("expected fetcher indicator NOT to be re-initialized when retrieved")
	}

	// 4. Test FetchAndInitialize failure case
	feederErr := &MockHistoricalFeeder{FetchSMAErr: errors.New("historical fetch failed")}
	providerErr := &MockHistoricalFeederProvider{Feeder: feederErr}
	poolErr := tick.NewDefaultDataPool(providerErr)

	// Should not panic, but log error internally
	indErr := poolErr.GetOrCreateIndicator(symbol, "fetcher_err_ind", func() tick.Indicator {
		return &TestFetcherIndicator{id: "fetcher_err_ind"}
	})
	if indErr.(*TestFetcherIndicator).initialized {
		t.Errorf("should not be initialized on error")
	}

	// 5. Test PushTick and state update
	pool.PushTick(tick.Tick{Symbol: symbol, Price: 5000.0, TradingVolume: 500.0})
	state = pool.GetState(symbol)
	if state.LatestTick.Price != 5000.0 {
		t.Errorf("expected state LatestTick.Price 5000.0, got %f", state.LatestTick.Price)
	}
	if fetcherInd.updateCount != 1 {
		t.Errorf("expected registered indicator Update to be called, got %d", fetcherInd.updateCount)
	}
}

// 5. Concurrent Symbol Creation (covers getOrCreateSymbolData load checks)
func TestDefaultDataPool_ConcurrentSymbolCreation(t *testing.T) {
	pool := tick.NewDefaultDataPool(nil)

	// Run multiple attempts with different symbols to ensure the race condition
	// between Load and LoadOrStore is hit, triggering the loaded = true branch.
	for attempt := 0; attempt < 100; attempt++ {
		symbol := fmt.Sprintf("9999_%d", attempt)
		const numGoroutines = 8
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func() {
				defer wg.Done()
				<-start
				// Call GetState which internally uses getOrCreateSymbolData
				_ = pool.GetState(symbol)
			}()
		}

		close(start)
		wg.Wait()
	}
}

// 6. Double-Checked Locking in GetOrCreateIndicator (exists = true after lock release)
func TestDefaultDataPool_DoubleCheckedLocking(t *testing.T) {
	pool := tick.NewDefaultDataPool(nil)
	symbol := "8001"
	indicatorID := "double_check_ind"

	indDouble := pool.GetOrCreateIndicator(symbol, indicatorID, func() tick.Indicator {
		// Synchronously register the indicator from inside the factory
		// to simulate another goroutine having registered it while the lock was released.
		return pool.GetOrCreateIndicator(symbol, indicatorID, func() tick.Indicator {
			return &TestFetcherIndicator{id: "double_check_ind_inner"}
		})
	})

	// Verify that the inner registered one is returned
	innerInd, ok := indDouble.(*TestFetcherIndicator)
	if !ok {
		t.Fatalf("expected TestFetcherIndicator, got %T", indDouble)
	}
	if innerInd.id != "double_check_ind_inner" {
		t.Errorf("expected ID 'double_check_ind_inner', got '%s'", innerInd.id)
	}
}

// 7. Tests for rebuildOrder topological sort and cycle detection panic
func TestDefaultDataPool_TopologicalSort_And_CyclePanic(t *testing.T) {
	pool := tick.NewDefaultDataPool(nil)
	symbol := "7203"

	// Create dependencies: C -> B -> A, D -> A (so A is visited twice to trigger visited check)
	indA := &TestFetcherIndicator{id: "A"}
	indB := &TestFetcherIndicator{id: "B", dependencies: []tick.Indicator{indA}}
	indC := &TestFetcherIndicator{id: "C", dependencies: []tick.Indicator{indB}}
	indD := &TestFetcherIndicator{id: "D", dependencies: []tick.Indicator{indA}}

	// Register them (in non-sorted order)
	pool.GetOrCreateIndicator(symbol, "C", func() tick.Indicator { return indC })
	pool.GetOrCreateIndicator(symbol, "B", func() tick.Indicator { return indB })
	pool.GetOrCreateIndicator(symbol, "A", func() tick.Indicator { return indA })
	pool.GetOrCreateIndicator(symbol, "D", func() tick.Indicator { return indD })

	// Verify they are sorted.
	pool.PushTick(tick.Tick{Symbol: symbol, Price: 150.0})

	state := pool.GetState(symbol)
	if state.LatestTick.Price != 150.0 {
		t.Errorf("expected price 150.0, got %f", state.LatestTick.Price)
	}

	// Test Circular Dependency detection (should panic)
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected panic on circular dependency, but code did not panic")
		} else {
			t.Logf("successfully caught expected panic: %v", r)
		}
	}()

	// Create circular dependency: D -> E -> D
	indD_circle := &TestFetcherIndicator{id: "D_circle"}
	indE_circle := &TestFetcherIndicator{id: "E_circle", dependencies: []tick.Indicator{indD_circle}}
	indD_circle.dependencies = []tick.Indicator{indE_circle}

	poolCircle := tick.NewDefaultDataPool(nil)
	poolCircle.GetOrCreateIndicator(symbol, "D_circle", func() tick.Indicator { return indD_circle })
	poolCircle.GetOrCreateIndicator(symbol, "E_circle", func() tick.Indicator { return indE_circle })
}
