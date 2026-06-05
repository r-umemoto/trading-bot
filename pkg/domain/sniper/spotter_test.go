package sniper

import (
	"log/slog"
	"testing"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/position"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

func TestObservation_HoldQty(t *testing.T) {
	obs := Observation{
		Positions: []position.Position{
			{LeavesQty: 10, Action: order.ACTION_BUY},
			{LeavesQty: 5, Action: order.ACTION_SELL},
		},
	}
	if obs.HoldQty() != 5 {
		t.Errorf("expected 5, got %f", obs.HoldQty())
	}
}

func TestSpotter_PnLAndPerformance(t *testing.T) {
	sym := symbol.Symbol{Code: "7203"}
	sp := NewSpotter(sym, slog.Default())
	sniperID := "sniper-1"

	// Add positions
	sp.sniperPositions[sniperID] = []position.Position{
		{LeavesQty: 10, Price: 2000, Action: order.ACTION_BUY},
		{LeavesQty: 5, Price: 2010, Action: order.ACTION_SELL},
	}

	// 1. GetUnrealizedPnL
	// Buy position: (2020 - 2000) * 10 = 200
	// Sell position: (2020 - 2010) * 5 * -1 = -50
	// Total: 150
	pnl := sp.GetUnrealizedPnL(sniperID, 2020)
	if pnl != 150.0 {
		t.Errorf("expected UnrealizedPnL 150.0, got %f", pnl)
	}

	// 2. GetPerformance / HoldQty
	if sp.HoldQty(sniperID) != 5 {
		t.Errorf("expected HoldQty 5, got %f", sp.HoldQty(sniperID))
	}
	perf := sp.GetPerformance(sniperID)
	if perf.Trades != 0 {
		t.Errorf("expected 0 trades initially, got %d", perf.Trades)
	}
}

func TestSpotter_RevertOrderStatus(t *testing.T) {
	sym := symbol.Symbol{Code: "7203"}
	sp := NewSpotter(sym, nil)
	sniperID := "sniper-1"

	ord := order.NewOrder("order-1", "7203", order.ACTION_BUY, 2000, 100)
	sp.AddOrder(sniperID, ord)

	sp.RevertOrderStatus(sniperID, ord, order.ORDER_STATUS_CANCEL_SENT)
	if ord.Status() != order.ORDER_STATUS_CANCEL_SENT {
		t.Errorf("expected status reverted to CANCEL_SENT, got %v", ord.Status())
	}
}

func TestSpotter_PrepareObservation_IfDoneChild(t *testing.T) {
	sym := symbol.Symbol{Code: "7203"}
	sp := NewSpotter(sym, nil)
	sniperID := "sniper-1"

	childOrd := order.NewOrder("child", "7203", order.ACTION_SELL, 2100, 100)
	parentOrd := order.NewOrder("parent", "7203", order.ACTION_BUY, 2000, 100)
	parentOrd.IfDone = childOrd
	parentOrd.BypassTransition(order.ORDER_STATUS_FILLED, order.STATE_CLOSED)

	sp.AddOrder(sniperID, parentOrd)

	// PrepareObservation should notice completed parentOrd, start tracking childOrd, and set hasProcessingTrade
	obs := sp.PrepareObservation(sniperID, tick.Tick{Price: 2000}, nil)
	if len(obs.ActiveOrders) != 1 || obs.ActiveOrders[0].ID != "child" {
		t.Errorf("expected child order to be tracked, got: %v", obs.ActiveOrders)
	}
	if !obs.HasProcessingTrade {
		t.Error("expected HasProcessingTrade to be true")
	}
}

func TestSpotter_ApplyExecution_DuplicateAndRequestFallbacks(t *testing.T) {
	sym := symbol.Symbol{Code: "7203"}
	sp := NewSpotter(sym, nil)
	sniperID := "sniper-1"

	exec := order.Execution{
		ID:            "exec-1",
		Price:         2000,
		Qty:           100,
		ExecutionTime: time.Now(),
	}

	// 1. First execution (no parentOrder)
	sp.applyExecution(sniperID, exec, order.ACTION_BUY, time.Now(), nil)
	if len(sp.sniperPositions[sniperID]) != 1 {
		t.Fatalf("expected 1 position, got %d", len(sp.sniperPositions[sniperID]))
	}

	// 2. Duplicate execution (should be ignored)
	sp.applyExecution(sniperID, exec, order.ACTION_BUY, time.Now(), nil)
	if len(sp.sniperPositions[sniperID]) != 1 {
		t.Errorf("expected duplicate execution to be ignored, got positions: %d", len(sp.sniperPositions[sniperID]))
	}
}

func TestSpotter_ReducePositions_SpecifiedClose(t *testing.T) {
	sym := symbol.Symbol{Code: "7203"}
	sp := NewSpotter(sym, nil)
	sniperID := "sniper-1"

	// Setup positions
	sp.sniperPositions[sniperID] = []position.Position{
		{ExecutionID: "exec-1", Symbol: "7203", Price: 2000, LeavesQty: 100, Action: order.ACTION_BUY, Meta: position.PositionMeta{EntryTime: time.Now().Add(-1 * time.Hour)}},
		{ExecutionID: "exec-2", Symbol: "7203", Price: 2010, LeavesQty: 50, Action: order.ACTION_BUY, Meta: position.PositionMeta{EntryTime: time.Now()}},
	}

	// 1. New exit execution
	exec := order.Execution{
		ID:            "exec-exit-1",
		Price:         2020,
		Qty:           120, // closes exec-2 fully (50), exec-1 partially (70)
		ExecutionTime: time.Now(),
	}

	parentOrder := order.NewOrder("exit-order", "7203", order.ACTION_SELL, 2020, 120)
	parentOrder.CashMargin = order.CASH_MARGIN_MARGIN_EXIT
	parentOrder.Request = &order.OrderRequest{
		ClosePositions: []order.ClosePosition{
			{HoldID: "exec-2", Qty: 50},
			{HoldID: "exec-1", Qty: 70},
		},
	}

	sp.applyExecution(sniperID, exec, order.ACTION_SELL, time.Now(), parentOrder)

	// exec-2 fully closed (deleted), exec-1 has 30 remaining
	remaining := sp.sniperPositions[sniperID]
	if len(remaining) != 1 || remaining[0].ExecutionID != "exec-1" || remaining[0].LeavesQty != 30 {
		t.Errorf("unexpected remaining positions: %+v", remaining)
	}

	// Performance verification
	// PnL exec-2: (2020 - 2010) * 50 = 500
	// PnL exec-1: (2020 - 2000) * 70 = 1400
	// Total: 1900
	perf := sp.GetPerformance(sniperID)
	if perf.RealizedPnL != 1900 || perf.Trades != 2 || perf.Wins != 2 {
		t.Errorf("unexpected performance: %+v", perf)
	}
}

func TestSpotter_ReducePositions_FIFOAndLossPerformance(t *testing.T) {
	sym := symbol.Symbol{Code: "7203"}
	sp := NewSpotter(sym, nil)
	sniperID := "sniper-1"

	// Setup positions (exec-1 is older, exec-2 is newer)
	sp.sniperPositions[sniperID] = []position.Position{
		{ExecutionID: "exec-1", Symbol: "7203", Price: 2000, LeavesQty: 100, Action: order.ACTION_BUY, Meta: position.PositionMeta{EntryTime: time.Now().Add(-1 * time.Hour)}},
		{ExecutionID: "exec-2", Symbol: "7203", Price: 2010, LeavesQty: 50, Action: order.ACTION_BUY, Meta: position.PositionMeta{EntryTime: time.Now()}},
	}

	// Exit Execution of 120 qty (FIFO: closes exec-1 fully (100) and exec-2 partially (20))
	exec := order.Execution{
		ID:            "exec-exit-1",
		Price:         1990, // Loss exit
		Qty:           120,
		ExecutionTime: time.Now(),
	}

	parentOrder := order.NewOrder("exit-order", "7203", order.ACTION_SELL, 1990, 120)
	parentOrder.CashMargin = order.CASH_MARGIN_MARGIN_EXIT

	sp.applyExecution(sniperID, exec, order.ACTION_SELL, time.Now(), parentOrder)

	// Remaining: exec-2 with 30 qty
	remaining := sp.sniperPositions[sniperID]
	if len(remaining) != 1 || remaining[0].ExecutionID != "exec-2" || remaining[0].LeavesQty != 30 {
		t.Errorf("unexpected remaining positions: %+v", remaining)
	}

	// Performance verification
	// PnL exec-1: (1990 - 2000) * 100 = -1000
	// PnL exec-2: (1990 - 2010) * 20 = -400
	// Total realized: -1400 (2 losses)
	perf := sp.GetPerformance(sniperID)
	if perf.RealizedPnL != -1400 || perf.Trades != 2 || perf.Losses != 2 {
		t.Errorf("unexpected performance: %+v", perf)
	}
}

func TestSpotter_ReducePositions_FlatPnL(t *testing.T) {
	sym := symbol.Symbol{Code: "7203"}
	sp := NewSpotter(sym, nil)
	sniperID := "sniper-1"

	sp.sniperPositions[sniperID] = []position.Position{
		{ExecutionID: "exec-1", Symbol: "7203", Price: 2000, LeavesQty: 100, Action: order.ACTION_BUY},
	}

	exec := order.Execution{
		ID:    "exec-exit-1",
		Price: 2000, // flat
		Qty:   100,
	}

	parentOrder := order.NewOrder("exit-order", "7203", order.ACTION_SELL, 2000, 100)
	parentOrder.CashMargin = order.CASH_MARGIN_MARGIN_EXIT

	sp.applyExecution(sniperID, exec, order.ACTION_SELL, time.Now(), parentOrder)

	perf := sp.GetPerformance(sniperID)
	if perf.RealizedPnL != 0.0 || perf.Trades != 1 || perf.Wins != 0 || perf.Losses != 0 {
		t.Errorf("unexpected performance: %+v", perf)
	}
}
