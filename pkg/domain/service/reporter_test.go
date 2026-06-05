package service_test

import (
	"testing"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/service"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

type mockPerformanceProvider struct {
	performances   map[string]sniper.Performance
	unrealizedPnLs map[string]float64
}

func (m *mockPerformanceProvider) GetPerformance(id string) sniper.Performance {
	return m.performances[id]
}

func (m *mockPerformanceProvider) GetUnrealizedPnL(id string, currentPrice float64) float64 {
	return m.unrealizedPnLs[id] * currentPrice
}

type mockReportableTarget struct {
	id           string
	symbol       string
	strategyName string
}

func (m *mockReportableTarget) GetID() string           { return m.id }
func (m *mockReportableTarget) GetSymbolCode() string   { return m.symbol }
func (m *mockReportableTarget) GetStrategyName() string { return m.strategyName }

type mockDataPool struct {
	states map[string]tick.MarketState
}

func (m *mockDataPool) PushTick(t tick.Tick) {}
func (m *mockDataPool) GetState(symbol string) tick.MarketState {
	return m.states[symbol]
}
func (m *mockDataPool) GetOrCreateIndicator(symbol, id string, factory func() tick.Indicator) tick.Indicator {
	return nil
}

func TestGeneratePerformanceReport(t *testing.T) {
	// Setup mock data
	provider := &mockPerformanceProvider{
		performances: map[string]sniper.Performance{
			"target-1": {Trades: 10, Wins: 6, Losses: 4, RealizedPnL: 1000.0},
			"target-2": {Trades: 5, Wins: 3, Losses: 2, RealizedPnL: 500.0},
			"target-3": {Trades: 2, Wins: 1, Losses: 1, RealizedPnL: -100.0},
		},
		unrealizedPnLs: map[string]float64{
			"target-1": 1.5, // PnL = 1.5 * price
			"target-2": -0.5,
			"target-3": 2.0,
		},
	}

	targets := []sniper.ReportableTarget{
		&mockReportableTarget{id: "target-1", symbol: "7203", strategyName: "StratA"},
		&mockReportableTarget{id: "target-2", symbol: "7203", strategyName: "StratB"},
		&mockReportableTarget{id: "target-3", symbol: "9984", strategyName: "StratA"},
	}

	dataPool := &mockDataPool{
		states: map[string]tick.MarketState{
			"7203": {
				Symbol: "7203",
				LatestTick: tick.Tick{
					Price:            2000.0,
					CurrentPriceTime: time.Now(),
				},
			},
			// 9984 has zero CurrentPriceTime (should skip unrealized PnL calculation)
			"9984": {
				Symbol: "9984",
				LatestTick: tick.Tick{
					Price: 5000.0,
				},
			},
		},
	}

	// Generate report
	report := service.GeneratePerformanceReport(provider, targets, dataPool)

	if report == nil {
		t.Fatal("expected report not to be nil")
	}

	// Check Total
	// target-1: Realized=1000, Unrealized = 1.5 * 2000 = 3000
	// target-2: Realized=500, Unrealized = -0.5 * 2000 = -1000
	// target-3: Realized=-100, Unrealized = 0 (due to zero time)
	// Total Trades: 10 + 5 + 2 = 17
	// Total Wins: 6 + 3 + 1 = 10
	// Total Losses: 4 + 2 + 1 = 7
	// Total RealizedPnL: 1000 + 500 - 100 = 1400
	// Total UnrealizedPnL: 3000 - 1000 + 0 = 2000
	if report.Total.Trades != 17 {
		t.Errorf("expected Total.Trades 17, got %d", report.Total.Trades)
	}
	if report.Total.Wins != 10 {
		t.Errorf("expected Total.Wins 10, got %d", report.Total.Wins)
	}
	if report.Total.Losses != 7 {
		t.Errorf("expected Total.Losses 7, got %d", report.Total.Losses)
	}
	if report.Total.RealizedPnL != 1400.0 {
		t.Errorf("expected Total.RealizedPnL 1400.0, got %f", report.Total.RealizedPnL)
	}
	if report.Total.UnrealizedPnL != 2000.0 {
		t.Errorf("expected Total.UnrealizedPnL 2000.0, got %f", report.Total.UnrealizedPnL)
	}

	// Check Symbols
	// 7203 (target-1 + target-2): Trades=15, Realized=1500, Unrealized=2000
	// 9984 (target-3): Trades=2, Realized=-100, Unrealized=0
	perf7203 := report.Symbols["7203"]
	if perf7203 == nil {
		t.Fatal("expected performance for symbol 7203")
	}
	if perf7203.Trades != 15 || perf7203.RealizedPnL != 1500.0 || perf7203.UnrealizedPnL != 2000.0 {
		t.Errorf("unexpected 7203 metrics: %+v", perf7203)
	}

	perf9984 := report.Symbols["9984"]
	if perf9984 == nil {
		t.Fatal("expected performance for symbol 9984")
	}
	if perf9984.Trades != 2 || perf9984.RealizedPnL != -100.0 || perf9984.UnrealizedPnL != 0.0 {
		t.Errorf("unexpected 9984 metrics: %+v", perf9984)
	}

	// Check Strats
	// StratA (target-1 + target-3): Trades=12, Realized=900, Unrealized=3000
	// StratB (target-2): Trades=5, Realized=500, Unrealized=-1000
	stratA := report.Strats["StratA"]
	if stratA == nil {
		t.Fatal("expected performance for strategy StratA")
	}
	if stratA.Trades != 12 || stratA.RealizedPnL != 900.0 || stratA.UnrealizedPnL != 3000.0 {
		t.Errorf("unexpected StratA metrics: %+v", stratA)
	}

	// Check Combined
	// "7203|StratA" (target-1): Realized=1000, Unrealized=3000
	comb1 := report.Combined["7203|StratA"]
	if comb1 == nil {
		t.Fatal("expected performance for combined key 7203|StratA")
	}
	if comb1.Name != "7203 x StratA" {
		t.Errorf("expected combined Name '7203 x StratA', got '%s'", comb1.Name)
	}
	if comb1.RealizedPnL != 1000.0 || comb1.UnrealizedPnL != 3000.0 {
		t.Errorf("unexpected combined metrics: %+v", comb1)
	}
}

// Dummy struct to reference order package to satisfy unused import rule if any, but order is used
var _ = order.Action(order.ACTION_BUY)
