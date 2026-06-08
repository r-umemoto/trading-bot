package strategy_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

type dummyDataPool struct {
	indicator tick.Indicator
}

func (d *dummyDataPool) PushTick(t tick.Tick) {}
func (d *dummyDataPool) GetState(symbol string) tick.MarketState {
	return tick.MarketState{}
}
func (d *dummyDataPool) GetOrCreateIndicator(symbol, id string, factory func() tick.Indicator) tick.Indicator {
	if d.indicator == nil {
		d.indicator = factory()
	}
	return d.indicator
}

func TestSampleStrategy_Lifecycle(t *testing.T) {
	factory, err := strategy.GetFactory("sample")
	if err != nil {
		t.Fatalf("failed to get sample strategy factory: %v", err)
	}

	sym := symbol.Symbol{Code: "7203"}
	dp := &dummyDataPool{}

	s := factory.NewStrategy(sym, dp, nil)
	if s.Name() != "sample" {
		t.Errorf("expected strategy name 'sample', got '%s'", s.Name())
	}

	var expectedLogger *slog.Logger
	if s.AnalysisLogger() != expectedLogger {
		t.Errorf("expected nil logger")
	}

	policy := factory.CreateExecutionPolicy(nil)
	if _, ok := policy.(*strategy.NoopPolicy); !ok {
		t.Errorf("expected NoopPolicy execution policy")
	}
}

func TestSampleStrategy_Evaluate(t *testing.T) {
	sym := symbol.Symbol{Code: "7203"}
	dp := &dummyDataPool{}
	factory, _ := strategy.GetFactory("sample")
	s := factory.NewStrategy(sym, dp, nil)

	t.Run("Non-execution tick", func(t *testing.T) {
		input := strategy.StrategyInput{
			LatestTick: tick.Tick{
				Price: 2000,
				// CurrentPriceStatus is NONE, so IsExecution() returns false
			},
			Position: strategy.Position{Qty: 50, AveragePrice: 2000},
		}
		target := s.Evaluate(input)
		if target.Qty != 50 {
			t.Errorf("expected Qty 50, got %f", target.Qty)
		}
	})

	t.Run("Long position - trailing stop and loss cut", func(t *testing.T) {
		// Reset/Recreate strategy to clear highPrice state
		s = factory.NewStrategy(sym, dp, nil)

		// 1. Initial execution tick: sets highPrice to 2000
		input1 := strategy.StrategyInput{
			LatestTick: tick.Tick{
				Price:              2000,
				CurrentPriceTime:   time.Now(),
				CurrentPriceStatus: tick.PRICE_STATUS_CURRENT,
				TradingVolume:      100,
			},
			Position: strategy.Position{Qty: 100, AveragePrice: 2000},
		}
		target1 := s.Evaluate(input1)
		if target1.Qty != 100 {
			t.Errorf("expected Qty 100, got %f", target1.Qty)
		}

		// 2. Price rise: updates highPrice to 2500
		input2 := strategy.StrategyInput{
			LatestTick: tick.Tick{
				Price:              2500,
				CurrentPriceTime:   time.Now(),
				CurrentPriceStatus: tick.PRICE_STATUS_CURRENT,
				TradingVolume:      200,
			},
			Position: strategy.Position{Qty: 100, AveragePrice: 2000},
		}
		s.Evaluate(input2)

		// 3. Trailing Stop trigger: price drops below highPrice * 0.80 (2500 * 0.80 = 2000)
		// e.g. price = 1900, which is also below average price (2000)
		input3 := strategy.StrategyInput{
			LatestTick: tick.Tick{
				Price:              1900,
				CurrentPriceTime:   time.Now(),
				CurrentPriceStatus: tick.PRICE_STATUS_CURRENT,
				TradingVolume:      300,
			},
			Position: strategy.Position{Qty: 100, AveragePrice: 2000},
		}
		target3 := s.Evaluate(input3)
		if target3.Qty != 0 || target3.Reason != "trailing stop" {
			t.Errorf("expected trailing stop signal, got Qty %f, Reason '%s'", target3.Qty, target3.Reason)
		}

		// Reset/Recreate to test Loss Cut
		s = factory.NewStrategy(sym, dp, nil)
		// Set highPrice first
		s.Evaluate(input1)

		// Loss cut trigger: price drops below avgPrice * 0.997 (2000 * 0.997 = 1994)
		// but above highPrice * 0.80 (2000 * 0.80 = 1600)
		input4 := strategy.StrategyInput{
			LatestTick: tick.Tick{
				Price:              1990,
				CurrentPriceTime:   time.Now(),
				CurrentPriceStatus: tick.PRICE_STATUS_CURRENT,
				TradingVolume:      400,
			},
			Position: strategy.Position{Qty: 100, AveragePrice: 2000},
		}
		target4 := s.Evaluate(input4)
		if target4.Qty != 0 || target4.Reason != "loss cut" {
			t.Errorf("expected loss cut signal, got Qty %f, Reason '%s'", target4.Qty, target4.Reason)
		}
	})

	t.Run("No position - consecutive bar rises", func(t *testing.T) {
		// Recreate strategy to get access to 1min indicator
		s = factory.NewStrategy(sym, dp, nil)
		indicator := dp.indicator.(*tick.OneMinBarIndicator)

		input := strategy.StrategyInput{
			LatestTick: tick.Tick{
				Price:              2000,
				CurrentPriceTime:   time.Now(),
				CurrentPriceStatus: tick.PRICE_STATUS_CURRENT,
				TradingVolume:      1000,
			},
			Position: strategy.Position{Qty: 0, AveragePrice: 0},
		}

		// 1. Less than 3 bars -> should do nothing
		target1 := s.Evaluate(input)
		if target1.Qty != 0 {
			t.Errorf("expected Qty 0 with less than 3 bars")
		}

		baseTime := time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC)

		// Push bars (not consecutive rise)
		indicator.Update(tick.Tick{Price: 100, CurrentPriceTime: baseTime, TradingVolume: 100})
		indicator.Update(tick.Tick{Price: 90, CurrentPriceTime: baseTime.Add(1 * time.Minute), TradingVolume: 200})
		indicator.Update(tick.Tick{Price: 95, CurrentPriceTime: baseTime.Add(2 * time.Minute), TradingVolume: 300})

		target2 := s.Evaluate(input)
		if target2.Qty != 0 {
			t.Errorf("expected Qty 0 when bars do not rise consecutively")
		}

		// Push rising bars (recreate or update with higher values)
		// To reset, recreate strategy and dummyDataPool
		dp2 := &dummyDataPool{}
		s = factory.NewStrategy(sym, dp2, nil)
		indicator2 := dp2.indicator.(*tick.OneMinBarIndicator)

		indicator2.Update(tick.Tick{Price: 100, CurrentPriceTime: baseTime, TradingVolume: 100})
		indicator2.Update(tick.Tick{Price: 105, CurrentPriceTime: baseTime.Add(1 * time.Minute), TradingVolume: 200})
		indicator2.Update(tick.Tick{Price: 110, CurrentPriceTime: baseTime.Add(2 * time.Minute), TradingVolume: 300})

		target3 := s.Evaluate(input)
		if target3.Qty != 100 || target3.OrderType != order.ORDER_TYPE_MARKET || target3.Reason != "3 consecutive bars rise" {
			t.Errorf("expected buy signal on 3 consecutive bars rise, got %+v", target3)
		}
	})
}
