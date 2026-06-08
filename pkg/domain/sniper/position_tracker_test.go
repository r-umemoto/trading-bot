package sniper_test

import (
	"testing"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/position"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
)

func TestPositionTracker_ApplyExecution_Entry(t *testing.T) {
	pt := sniper.NewPositionTracker(nil)
	sniperID := "test-sniper"

	// 1. Entry Buy with nil parentOrder (uses defaults)
	exec1 := order.Execution{
		ID:            "exec-1",
		Qty:           100,
		Price:         2000,
		ExecutionTime: time.Now(),
	}
	pt.ApplyExecution(sniperID, "7203", exec1, order.ACTION_BUY, nil, func(pnl float64) {})

	positions := pt.GetCopy(sniperID)
	if len(positions) != 1 {
		t.Fatalf("expected 1 position, got %d", len(positions))
	}
	p := positions[0]
	if p.ExecutionID != "exec-1" || p.Exchange != order.EXCHANGE_TOSHO || p.LeavesQty != 100 || p.Price != 2000 {
		t.Errorf("unexpected position values: %+v", p)
	}

	// 2. Entry Sell with parentOrder custom properties
	exec2 := order.Execution{
		ID:            "exec-2",
		Qty:           50,
		Price:         2010,
		ExecutionTime: time.Now(),
	}
	parent := order.NewOrder("order-parent", "7203", order.ACTION_SELL, 2010, 50)
	parent.CashMargin = order.CASH_MARGIN_MARGIN_ENTRY
	parent.Request = &order.OrderRequest{
		Exchange:        order.EXCHANGE_SOR,
		MarginTradeType: order.TRADE_TYPE_GENERAL,
		AccountType:     order.ACCOUNT_GENERAL,
	}

	pt.ApplyExecution(sniperID, "7203", exec2, order.ACTION_SELL, parent, func(pnl float64) {})

	positions = pt.GetCopy(sniperID)
	if len(positions) != 2 {
		t.Fatalf("expected 2 positions, got %d", len(positions))
	}
	p2 := positions[1]
	if p2.ExecutionID != "exec-2" || p2.Exchange != order.EXCHANGE_SOR || p2.TradeType != order.TRADE_TYPE_GENERAL || p2.AccountType != order.ACCOUNT_GENERAL {
		t.Errorf("unexpected position custom properties: %+v", p2)
	}
}

func TestPositionTracker_ApplyExecution_Exit_FIFO(t *testing.T) {
	pt := sniper.NewPositionTracker(nil)
	sniperID := "test-sniper"

	now := time.Now()

	// Setup three Buy positions (Long)
	pt.ApplyExecution(sniperID, "7203", order.Execution{ID: "exec-1", Qty: 100, Price: 2000, ExecutionTime: now.Add(-10 * time.Minute)}, order.ACTION_BUY, nil, func(pnl float64) {})
	pt.ApplyExecution(sniperID, "7203", order.Execution{ID: "exec-2", Qty: 100, Price: 2010, ExecutionTime: now.Add(-5 * time.Minute)}, order.ACTION_BUY, nil, func(pnl float64) {})
	pt.ApplyExecution(sniperID, "7203", order.Execution{ID: "exec-3", Qty: 100, Price: 2020, ExecutionTime: now.Add(-1 * time.Minute)}, order.ACTION_BUY, nil, func(pnl float64) {})

	// Exit (Sell) with FIFO reduction (leavesQty = 150)
	// Closes full exec-1 (100 qty @ 2000 -> PnL: (2020-2000)*100 = 2000)
	// Closes partial exec-2 (50 qty @ 2010 -> PnL: (2020-2010)*50 = 500)
	// Leaves exec-3 untouched (triggers remainingToSell <= 0)
	// Total PnL: 2500
	exitParent := order.NewOrder("order-exit", "7203", order.ACTION_SELL, 2020, 150)
	exitParent.CashMargin = order.CASH_MARGIN_MARGIN_EXIT

	var pnlCalls []float64
	pt.ApplyExecution(sniperID, "7203", order.Execution{ID: "exit-exec", Qty: 150, Price: 2020, ExecutionTime: now}, order.ACTION_SELL, exitParent, func(pnl float64) {
		pnlCalls = append(pnlCalls, pnl)
	})

	positions := pt.GetCopy(sniperID)
	if len(positions) != 2 {
		t.Fatalf("expected 2 remaining positions, got %d", len(positions))
	}
	if positions[0].ExecutionID != "exec-2" || positions[0].LeavesQty != 50 {
		t.Errorf("unexpected remaining position 1: %+v", positions[0])
	}
	if positions[1].ExecutionID != "exec-3" || positions[1].LeavesQty != 100 {
		t.Errorf("unexpected remaining position 2: %+v", positions[1])
	}

	var totalPnL float64
	for _, p := range pnlCalls {
		totalPnL += p
	}
	if totalPnL != 2500 {
		t.Errorf("expected total realized PnL of 2500, got %f", totalPnL)
	}

	// Short Position FIFO Exit (Setup Short Position)
	shortPT := sniper.NewPositionTracker(nil)
	shortPT.ApplyExecution(sniperID, "7203", order.Execution{ID: "exec-short-1", Qty: 100, Price: 2000, ExecutionTime: now.Add(-10 * time.Minute)}, order.ACTION_SELL, nil, func(pnl float64) {})
	
	// Exit (Buy) with FIFO reduction (100 qty @ 1980 -> PnL: (1980-2000)*100*(-1) = 2000)
	buyExitParent := order.NewOrder("order-exit-buy", "7203", order.ACTION_BUY, 1980, 100)
	buyExitParent.CashMargin = order.CASH_MARGIN_MARGIN_EXIT

	var shortPnl float64
	shortPT.ApplyExecution(sniperID, "7203", order.Execution{ID: "exit-exec-buy", Qty: 100, Price: 1980, ExecutionTime: now}, order.ACTION_BUY, buyExitParent, func(pnl float64) {
		shortPnl += pnl
	})

	if shortPnl != 2000 {
		t.Errorf("expected realized PnL for short position exit to be 2000, got %f", shortPnl)
	}
}

func TestPositionTracker_ApplyExecution_Exit_ClosePositions(t *testing.T) {
	pt := sniper.NewPositionTracker(nil)
	sniperID := "test-sniper"
	now := time.Now()

	// Setup three Buy positions (Long)
	pt.ApplyExecution(sniperID, "7203", order.Execution{ID: "exec-1", Qty: 100, Price: 2000, ExecutionTime: now.Add(-10 * time.Minute)}, order.ACTION_BUY, nil, func(pnl float64) {})
	pt.ApplyExecution(sniperID, "7203", order.Execution{ID: "exec-2", Qty: 100, Price: 2010, ExecutionTime: now.Add(-5 * time.Minute)}, order.ACTION_BUY, nil, func(pnl float64) {})
	pt.ApplyExecution(sniperID, "7203", order.Execution{ID: "exec-3", Qty: 100, Price: 2020, ExecutionTime: now.Add(-1 * time.Minute)}, order.ACTION_BUY, nil, func(pnl float64) {})

	// Specific Exit using ClosePositions: Specifying to close exec-2 (100 qty) and exec-3 (80 qty, but bounded by remainingToSell 15)
	exitParent := order.NewOrder("order-exit-specific", "7203", order.ACTION_SELL, 2030, 115)
	exitParent.CashMargin = order.CASH_MARGIN_MARGIN_EXIT
	exitParent.Request = &order.OrderRequest{
		ClosePositions: []order.ClosePosition{
			{HoldID: "exec-2", Qty: 100},
			{HoldID: "exec-3", Qty: 80},
		},
	}

	var totalPnL float64
	pt.ApplyExecution(sniperID, "7203", order.Execution{ID: "exit-exec-specific", Qty: 115, Price: 2030, ExecutionTime: now}, order.ACTION_SELL, exitParent, func(pnl float64) {
		totalPnL += pnl
	})

	// Expected Remaining:
	// - exec-1: 100 qty (untouched)
	// - exec-3: 85 qty
	positions := pt.GetCopy(sniperID)
	if len(positions) != 2 {
		t.Fatalf("expected 2 remaining positions, got %d", len(positions))
	}

	posMap := make(map[string]position.Position)
	for _, p := range positions {
		posMap[p.ExecutionID] = p
	}

	if posMap["exec-1"].LeavesQty != 100 {
		t.Errorf("expected exec-1 qty to be 100, got %f", posMap["exec-1"].LeavesQty)
	}
	if _, exists := posMap["exec-2"]; exists {
		t.Errorf("expected exec-2 to be completely closed and removed")
	}
	if posMap["exec-3"].LeavesQty != 85 {
		t.Errorf("expected exec-3 qty to be 85, got %f", posMap["exec-3"].LeavesQty)
	}

	// Realized PnL:
	// exec-2: (2030 - 2010) * 100 = 2000
	// exec-3: (2030 - 2020) * 15 = 150
	// Total: 2150
	if totalPnL != 2150 {
		t.Errorf("expected total realized PnL of 2150, got %f", totalPnL)
	}

	// --- Specific Exit of Short positions ---
	shortPT := sniper.NewPositionTracker(nil)
	// Setup three Sell positions (Short)
	shortPT.ApplyExecution(sniperID, "7203", order.Execution{ID: "short-exec-1", Qty: 100, Price: 2050, ExecutionTime: now.Add(-10 * time.Minute)}, order.ACTION_SELL, nil, func(pnl float64) {})

	// Specific Exit using ClosePositions specifying short-exec-1 (50 qty)
	buyExitParent := order.NewOrder("order-exit-buy-specific", "7203", order.ACTION_BUY, 2030, 50)
	buyExitParent.CashMargin = order.CASH_MARGIN_MARGIN_EXIT
	buyExitParent.Request = &order.OrderRequest{
		ClosePositions: []order.ClosePosition{
			{HoldID: "short-exec-1", Qty: 50},
		},
	}

	var shortTotalPnL float64
	shortPT.ApplyExecution(sniperID, "7203", order.Execution{ID: "exit-short-exec-specific", Qty: 50, Price: 2030, ExecutionTime: now}, order.ACTION_BUY, buyExitParent, func(pnl float64) {
		shortTotalPnL += pnl
	})

	// Realized PnL:
	// short-exec-1: (2030 - 2050) * 50 * (-1) = 1000
	if shortTotalPnL != 1000 {
		t.Errorf("expected short total realized PnL of 1000, got %f", shortTotalPnL)
	}
}

func TestPositionTracker_HoldQty(t *testing.T) {
	pt := sniper.NewPositionTracker(nil)
	sniperID := "test-sniper"

	if pt.HoldQty(sniperID) != 0.0 {
		t.Errorf("expected 0 holding qty, got %f", pt.HoldQty(sniperID))
	}

	// Buy position (Long +100)
	pt.ApplyExecution(sniperID, "7203", order.Execution{ID: "exec-1", Qty: 100, Price: 2000}, order.ACTION_BUY, nil, func(pnl float64) {})
	// Sell position (Short -50)
	pt.ApplyExecution(sniperID, "7203", order.Execution{ID: "exec-2", Qty: 50, Price: 2010}, order.ACTION_SELL, nil, func(pnl float64) {})

	if pt.HoldQty(sniperID) != 50.0 {
		t.Errorf("expected 50 holding qty, got %f", pt.HoldQty(sniperID))
	}
}

func TestPositionTracker_GetUnrealizedPnL(t *testing.T) {
	pt := sniper.NewPositionTracker(nil)
	sniperID := "test-sniper"

	// Setup Long (qty 100 @ 2000) and Short (qty 50 @ 2050)
	pt.ApplyExecution(sniperID, "7203", order.Execution{ID: "long", Qty: 100, Price: 2000}, order.ACTION_BUY, nil, func(pnl float64) {})
	pt.ApplyExecution(sniperID, "7203", order.Execution{ID: "short", Qty: 50, Price: 2050}, order.ACTION_SELL, nil, func(pnl float64) {})

	// Market Price: 2020
	// Long PnL: (2020 - 2000) * 100 = 2000
	// Short PnL: (2020 - 2050) * 50 * (-1) = 1500
	// Total Unrealized: 3500
	unrealized := pt.GetUnrealizedPnL(sniperID, 2020)
	if unrealized != 3500 {
		t.Errorf("expected unrealized PnL to be 3500, got %f", unrealized)
	}
}

func TestPositionTracker_MatchPositionsToClose(t *testing.T) {
	pt := sniper.NewPositionTracker(nil)
	sniperID := "test-sniper"

	// Setup positions
	pt.ApplyExecution(sniperID, "7203", order.Execution{ID: "buy-1", Qty: 100, Price: 2000}, order.ACTION_BUY, nil, func(pnl float64) {})
	pt.ApplyExecution(sniperID, "7203", order.Execution{ID: "sell-1", Qty: 50, Price: 2050}, order.ACTION_SELL, nil, func(pnl float64) {})
	pt.ApplyExecution(sniperID, "7203", order.Execution{ID: "buy-2", Qty: 80, Price: 2010}, order.ACTION_BUY, nil, func(pnl float64) {})
	pt.ApplyExecution(sniperID, "7203", order.Execution{ID: "buy-3", Qty: 80, Price: 2015}, order.ACTION_BUY, nil, func(pnl float64) {})

	locked := map[string]bool{
		"buy-1": true, // buy-1 is locked/blocked
	}

	// We want to sell 50 shares. This means we must close existing Buy positions.
	// buy-1 is locked, buy-2 has 80, so buy-2 will fulfill the 50 shares completely.
	// This will cause remainingQty to become <= 0 and trigger the break condition when checking buy-3.
	closePos, orderType := pt.MatchPositionsToClose(sniperID, order.ACTION_SELL, 50, locked)
	if orderType != order.CLOSE_POSITION_ORDER_NONE {
		t.Errorf("unexpected close position order type: %v", orderType)
	}

	if len(closePos) != 1 {
		t.Fatalf("expected 1 matched close position, got %d", len(closePos))
	}

	if closePos[0].HoldID != "buy-2" || closePos[0].Qty != 50 {
		t.Errorf("unexpected matched position: %+v", closePos[0])
	}
}

func TestPositionTracker_GetCopy(t *testing.T) {
	pt := sniper.NewPositionTracker(nil)
	sniperID := "test-sniper"

	pt.ApplyExecution(sniperID, "7203", order.Execution{ID: "buy-1", Qty: 100, Price: 2000}, order.ACTION_BUY, nil, func(pnl float64) {})

	copy1 := pt.GetCopy(sniperID)
	copy1[0].LeavesQty = 0 // mutate copy

	copy2 := pt.GetCopy(sniperID)
	if copy2[0].LeavesQty != 100 {
		t.Error("mutating copy affected internal state of PositionTracker")
	}
}
