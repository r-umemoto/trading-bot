package sniper

import (
	"log/slog"
	"testing"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/brain"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

type mockNestStrategy struct {
	evaluateFn func(input strategy.StrategyInput) brain.Signal
}

func (m *mockNestStrategy) Name() string { return "nest-mock" }
func (m *mockNestStrategy) Evaluate(input strategy.StrategyInput) brain.Signal {
	if m.evaluateFn != nil {
		return m.evaluateFn(input)
	}
	return brain.Signal{}
}
func (m *mockNestStrategy) AnalysisLogger() *slog.Logger { return nil }
func (m *mockNestStrategy) IfDone(input strategy.StrategyInput, prevSignal brain.Signal) brain.Signal {
	return brain.Signal{Action: brain.ACTION_HOLD}
}
func (m *mockNestStrategy) ShouldCancel(input strategy.StrategyInput, ord *order.Order) bool {
	return false
}

func TestSniperNest(t *testing.T) {
	sym := symbol.Symbol{Code: "7203", Name: "Toyota"}
	sp := NewSpotter(sym, nil)

	strat := &mockNestStrategy{}
	s1 := NewSniper("sniper-1", sym, strat, &strategy.NoopPolicy{}, order.EXCHANGE_TOSHO, nil)
	s2 := NewSniper("sniper-2", sym, strat, &strategy.NoopPolicy{}, order.EXCHANGE_SOR, nil)

	nest := NewSniperNest("7203", sp, []*Sniper{s1, s2})

	// 1. GetSymbolCode and GetSymbolCodes
	if nest.GetSymbolCode() != "7203" {
		t.Errorf("expected GetSymbolCode '7203', got '%s'", nest.GetSymbolCode())
	}
	codes := nest.GetSymbolCodes()
	if len(codes) != 1 || codes[0] != "7203" {
		t.Errorf("unexpected GetSymbolCodes: %v", codes)
	}

	// 2. HasSniper
	if !nest.HasSniper("sniper-1") || !nest.HasSniper("sniper-2") {
		t.Error("expected nest to have sniper-1 and sniper-2")
	}
	if nest.HasSniper("unknown") {
		t.Error("expected nest not to have unknown sniper")
	}

	// 3. GetExchanges
	exchanges := nest.GetExchanges()
	if len(exchanges) != 2 {
		t.Fatalf("expected 2 exchanges, got %d", len(exchanges))
	}
	hasTosho, hasSor := false, false
	for _, ex := range exchanges {
		if ex == order.EXCHANGE_TOSHO {
			hasTosho = true
		}
		if ex == order.EXCHANGE_SOR {
			hasSor = true
		}
	}
	if !hasTosho || !hasSor {
		t.Errorf("unexpected exchanges list: %v", exchanges)
	}

	// 4. GetReportableTargets
	targets := nest.GetReportableTargets()
	if len(targets) != 2 {
		t.Fatalf("expected 2 reportable targets, got %d", len(targets))
	}
	if targets[0].GetID() != "sniper-1" || targets[1].GetID() != "sniper-2" {
		t.Errorf("unexpected reportable targets: %v", targets)
	}

	// 5. HandleTick
	strat.evaluateFn = func(input strategy.StrategyInput) brain.Signal {
		return brain.Signal{
			Action:    brain.ACTION_BUY,
			TradeType: brain.TradeEntry,
			Quantity:  10,
			Price:     2000,
			OrderType: order.ORDER_TYPE_LIMIT,
			Reason:    "TestBuy",
		}
	}

	actions := nest.HandleTick(tick.Tick{Symbol: "7203", Price: 2000, TradingVolume: 100})
	if len(actions) != 2 {
		t.Fatalf("expected 2 fire actions, got %d", len(actions))
	}

	strat.evaluateFn = nil

	// 6. GetActiveOrders
	activeOrders := nest.GetActiveOrders()
	if len(activeOrders) != 2 {
		t.Errorf("expected 2 active orders in spotter, got %d", len(activeOrders))
	}

	// 7. UpdateOrders
	completedOrder := activeOrders[0]
	completedOrder.BypassTransition(order.ORDER_STATUS_FILLED, order.STATE_CLOSED)
	completedOrder.CumQty = completedOrder.OrderQty

	report := order.Orders{
		Orders: []order.Order{*completedOrder},
	}
	nest.UpdateOrders(report)

	if len(nest.GetActiveOrders()) != 1 {
		t.Errorf("expected 1 active order after update, got %d", len(nest.GetActiveOrders()))
	}

	// 8. FailSendingOrder and UpdateOrderID
	failedOrder := nest.GetActiveOrders()[0]
	nest.FailSendingOrder("sniper-1", failedOrder)
	nest.FailSendingOrder("sniper-2", failedOrder)
	if len(nest.GetActiveOrders()) != 0 {
		t.Errorf("expected 0 active orders after fail sending, got %d", len(nest.GetActiveOrders()))
	}

	newOrd := order.NewOrder("temp-local", "7203", order.ACTION_BUY, 2000, 100)
	sp.AddOrder("sniper-1", newOrd)
	nest.UpdateOrderID("sniper-1", newOrd, "temp-api-id")
	if len(nest.GetActiveOrders()) != 1 || nest.GetActiveOrders()[0].ID != "temp-api-id" {
		t.Errorf("expected active order ID updated to temp-api-id, got %v", nest.GetActiveOrders())
	}

	// 9. GetPerformance and GetUnrealizedPnL
	perf := nest.GetPerformance("sniper-1")
	if perf.Trades != 0 {
		t.Errorf("expected 0 trades initially, got %d", perf.Trades)
	}
	pnl := nest.GetUnrealizedPnL("sniper-1", 2005.0)
	if pnl != 0.0 {
		t.Errorf("expected 0 unrealized PnL initially, got %f", pnl)
	}

	// 10. ForceExit
	nest.ForceExit()
	if s1.GetLifecycle() != LifecycleStopped || s2.GetLifecycle() != LifecycleStopped {
		t.Error("expected lifecycle to be Stopped after ForceExit")
	}

	// 11. Spotter
	if nest.Spotter() != sp {
		t.Error("expected Spotter() to return the correct spotter")
	}
}
