package sniper_test

import (
	"testing"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

func TestOrderTracker_BasicOperations(t *testing.T) {
	ot := sniper.NewOrderTracker(nil)
	sniperID := "test-sniper"

	// 1. Add and GetActive / GetAllActive
	ord := order.NewOrder("local-1", "7203", order.ACTION_BUY, 2000, 100)
	ot.Add(sniperID, ord)

	active := ot.GetActive(sniperID)
	if len(active) != 1 || active[0].ID != "local-1" {
		t.Errorf("expected 1 active order, got %v", active)
	}

	allActive := ot.GetAllActive()
	if len(allActive) != 1 || allActive[0].ID != "local-1" {
		t.Errorf("expected 1 all active order, got %v", allActive)
	}

	// 2. UpdateOrderID
	ot.UpdateOrderID(sniperID, ord, "api-1")
	if ord.ID != "api-1" {
		t.Errorf("expected ID to be updated to api-1, got %s", ord.ID)
	}
	active = ot.GetActive(sniperID)
	if len(active) != 1 || active[0].ID != "api-1" {
		t.Errorf("expected active order ID updated in tracker, got %v", active)
	}

	// 3. RevertOrderStatus
	ot.RevertOrderStatus(sniperID, ord, order.ORDER_STATUS_WAITING)
	if ord.Status() != order.ORDER_STATUS_WAITING {
		t.Errorf("expected status to revert to WAITING, got %v", ord.Status())
	}

	// 4. Execution deduplication
	if ot.IsExecutionProcessed("exec-1") {
		t.Error("expected exec-1 to not be processed yet")
	}
	ot.MarkExecutionProcessed("exec-1")
	if !ot.IsExecutionProcessed("exec-1") {
		t.Error("expected exec-1 to be processed after marking")
	}
}

func TestOrderTracker_FailOrder(t *testing.T) {
	ot := sniper.NewOrderTracker(nil)
	sniperID := "test-sniper"

	ord := order.NewOrder("local-1", "7203", order.ACTION_BUY, 2000, 100)
	ot.Add(sniperID, ord)

	// Non-existent order fail should return false
	ordUnknown := order.NewOrder("local-unknown", "7203", order.ACTION_BUY, 2000, 100)
	if ot.FailOrder(sniperID, ordUnknown) {
		t.Error("expected FailOrder for unknown order to return false")
	}

	// Active order fail should return true, transition, and move to tombstones
	if !ot.FailOrder(sniperID, ord) {
		t.Error("expected FailOrder for active order to return true")
	}
	if len(ot.GetActive(sniperID)) != 0 {
		t.Error("expected 0 active orders after fail")
	}
}

func TestOrderTracker_GetInflightStats(t *testing.T) {
	ot := sniper.NewOrderTracker(nil)
	sniperID := "test-sniper"

	// Nil element handling
	ot.Add(sniperID, nil)

	// 1. Preparing Entry Buy
	o1 := order.NewOrder("o1", "7203", order.ACTION_BUY, 2000, 100)
	o1.BypassTransition(order.ORDER_STATUS_WAITING, order.STATE_PREPARING)
	o1.CashMargin = order.CASH_MARGIN_MARGIN_ENTRY

	// 2. Outstanding Exit Buy with ClosePositions (exits lock)
	o2 := order.NewOrder("o2", "7203", order.ACTION_BUY, 2010, 50)
	o2.BypassTransition(order.ORDER_STATUS_IN_PROGRESS, order.STATE_ACTIVE)
	o2.CashMargin = order.CASH_MARGIN_MARGIN_EXIT
	o2.Request = &order.OrderRequest{
		ClosePositions: []order.ClosePosition{
			{HoldID: "exec-parent-1", Qty: 50},
		},
	}

	// 3. Outstanding Entry Sell (short entry)
	o3 := order.NewOrder("o3", "7203", order.ACTION_SELL, 1990, 30)
	o3.BypassTransition(order.ORDER_STATUS_IN_PROGRESS, order.STATE_ACTIVE)
	o3.CashMargin = order.CASH_MARGIN_MARGIN_ENTRY

	// 4. Cancel Sent Order
	o4 := order.NewOrder("o4", "7203", order.ACTION_BUY, 2000, 10)
	o4.BypassTransition(order.ORDER_STATUS_CANCEL_SENT, order.STATE_CANCELING)

	// 5. Parent order with executions and IfDone exit
	oParent := order.NewOrder("oParent", "7203", order.ACTION_BUY, 2000, 100)
	oParent.BypassTransition(order.ORDER_STATUS_IN_PROGRESS, order.STATE_ACTIVE)
	oParent.AddExecution(order.Execution{ID: "exec-child-buy", Qty: 80, Price: 2000})
	oParent.AddExecution(order.Execution{ID: "exec-parent-1", Qty: 50, Price: 2000}) // Already covered by o2 ClosePositions, should be ignored for exit

	oChildBuy := order.NewOrder("oChildBuy", "7203", order.ACTION_BUY, 2010, 100)
	oChildBuy.CashMargin = order.CASH_MARGIN_MARGIN_EXIT
	oParent.IfDone = oChildBuy

	// 6. Parent order (short) with IfDone exit (sell exit)
	oParentShort := order.NewOrder("oParentShort", "7203", order.ACTION_SELL, 2000, 100)
	oParentShort.BypassTransition(order.ORDER_STATUS_IN_PROGRESS, order.STATE_ACTIVE)
	oParentShort.AddExecution(order.Execution{ID: "exec-child-sell", Qty: 40, Price: 2000})

	oChildSell := order.NewOrder("oChildSell", "7203", order.ACTION_SELL, 1990, 100)
	oChildSell.CashMargin = order.CASH_MARGIN_MARGIN_EXIT
	oParentShort.IfDone = oChildSell

	// 7. Synthetic Fill Expected order with IfDone exit (sell exit)
	oSynth := order.NewOrder("oSynth", "7203", order.ACTION_BUY, 2000, 15)
	oSynth.BypassTransition(order.ORDER_STATUS_FILL_EXPECTED, order.STATE_ACTIVE)
	oSynth.CashMargin = order.CASH_MARGIN_MARGIN_ENTRY

	oSynthChild := order.NewOrder("oSynthChild", "7203", order.ACTION_SELL, 2020, 15)
	oSynthChild.CashMargin = order.CASH_MARGIN_MARGIN_EXIT
	oSynth.IfDone = oSynthChild

	// 8. Outstanding Exit Sell
	oExitSell := order.NewOrder("oExitSell", "7203", order.ACTION_SELL, 1990, 20)
	oExitSell.BypassTransition(order.ORDER_STATUS_IN_PROGRESS, order.STATE_ACTIVE)
	oExitSell.CashMargin = order.CASH_MARGIN_MARGIN_EXIT

	// 9. Synthetic Fill Expected order with IfDone exit (buy exit)
	oSynthShort := order.NewOrder("oSynthShort", "7203", order.ACTION_SELL, 2000, 25)
	oSynthShort.BypassTransition(order.ORDER_STATUS_FILL_EXPECTED, order.STATE_ACTIVE)
	oSynthShort.CashMargin = order.CASH_MARGIN_MARGIN_ENTRY

	oSynthChild2 := order.NewOrder("oSynthChild2", "7203", order.ACTION_BUY, 1980, 25)
	oSynthChild2.CashMargin = order.CASH_MARGIN_MARGIN_EXIT
	oSynthShort.IfDone = oSynthChild2

	ot.Add(sniperID, o1)
	ot.Add(sniperID, o2)
	ot.Add(sniperID, o3)
	ot.Add(sniperID, o4)
	ot.Add(sniperID, oParent)
	ot.Add(sniperID, oParentShort)
	ot.Add(sniperID, oSynth)
	ot.Add(sniperID, oExitSell)
	ot.Add(sniperID, oSynthShort)

	stats := ot.GetInflightStats(sniperID)

	// Inflight entries
	// o1 (100) + oParent (100) = 200
	if stats.InflightBuyEntry != 200 {
		t.Errorf("expected InflightBuyEntry 200, got %f", stats.InflightBuyEntry)
	}
	// o3 (30) + oParentShort (100) = 130
	if stats.InflightSellEntry != 130 {
		t.Errorf("expected InflightSellEntry 130, got %f", stats.InflightSellEntry)
	}

	// Inflight exits
	// o2 exit buy (50) + oParent exit buy (80) + oSynthShort exit buy (25) = 155
	if stats.InflightBuyExit != 155 {
		t.Errorf("expected InflightBuyExit 155, got %f", stats.InflightBuyExit)
	}
	// oParentShort exit sell (40) + oSynth exit sell (15) + oExitSell exit sell (20) = 75
	if stats.InflightSellExit != 75 {
		t.Errorf("expected InflightSellExit 75, got %f", stats.InflightSellExit)
	}

	// Canceling orders
	if len(stats.CancelingOrders) != 1 || stats.CancelingOrders[0].ID != "o4" {
		t.Errorf("expected canceling orders list to contain o4, got %v", stats.CancelingOrders)
	}

	// Preparing order
	if stats.PreparingOrder != o1 {
		t.Errorf("expected preparing order to be o1, got %+v", stats.PreparingOrder)
	}

	// Outstanding order
	if stats.OutstandingOrder == nil {
		t.Error("expected outstanding order not to be nil")
	}
}

func TestOrderTracker_Update(t *testing.T) {
	ot := sniper.NewOrderTracker(nil)
	sniperID := "test-sniper"
	sym := symbol.Symbol{Code: "7203"}

	// 1. Setup active order and tombstone
	ord := order.NewOrder("order-1", "7203", order.ACTION_BUY, 2000, 100)
	ord.BypassTransition(order.ORDER_STATUS_IN_PROGRESS, order.STATE_ACTIVE)
	ot.Add(sniperID, ord)

	// Add a tombstone order that should remain (younger than 30s)
	tombstoneOrd := order.NewOrder("tombstone-local", "7203", order.ACTION_BUY, 2000, 100)
	ot.Add(sniperID, tombstoneOrd)
	ot.FailOrder(sniperID, tombstoneOrd) // puts it in tombstones

	// 2. Setup report containing completed order (status FILLED) and execution
	reportOrd := order.NewOrder("order-1", "7203", order.ACTION_BUY, 2000, 100)
	reportOrd.BypassTransition(order.ORDER_STATUS_FILLED, order.STATE_CLOSED)
	reportOrd.Executions = []order.Execution{
		{ID: "exec-1", Price: 2000, Qty: 100, ExecutionTime: time.Now()},
	}

	report := order.Orders{
		Orders: []order.Order{*reportOrd},
	}

	executionCalled := false
	ot.Update(report, sym, time.Now(), func(sID string, exec order.Execution, act order.Action, createdAt time.Time, parentOrder *order.Order) {
		if sID != sniperID || exec.ID != "exec-1" || act != order.ACTION_BUY || parentOrder != ord {
			t.Errorf("unexpected execution callback parameters: sID=%s, exec=%+v, act=%s, parent=%+v", sID, exec, act, parentOrder)
		}
		executionCalled = true
	})

	if !executionCalled {
		t.Error("expected execution callback to be called")
	}

	// Check that the order is no longer active
	if len(ot.GetActive(sniperID)) != 0 {
		t.Error("expected order-1 to be pruned from active orders since it is completed")
	}

	// Tombstone should still be there (now < entry.deletedAt + 30s)
	// Now call Update with future time (now + 31s) to trigger tombstone cleanup
	ot.Update(report, sym, time.Now().Add(31*time.Second), func(sID string, exec order.Execution, act order.Action, createdAt time.Time, parentOrder *order.Order) {})
}

func TestOrderTracker_PrepareActiveOrders(t *testing.T) {
	ot := sniper.NewOrderTracker(nil)
	sniperID := "test-sniper"

	// Add preparing order
	ord := order.NewOrder("local-1", "7203", order.ACTION_BUY, 2000, 100)
	ot.Add(sniperID, ord)

	active, hasProcessing, blocking := ot.PrepareActiveOrders(sniperID, tick.Tick{Price: 2000}, &strategy.NoopPolicy{})
	if len(active) != 1 || active[0].ID != "local-1" {
		t.Errorf("expected 1 active order, got %v", active)
	}
	if hasProcessing {
		t.Error("expected hasProcessing to be false")
	}
	if blocking != nil {
		t.Errorf("expected blocking to be nil, got %+v", blocking)
	}

	// The status should remain preparing
	if ord.InternalState() != order.STATE_PREPARING {
		t.Errorf("expected internal state to remain STATE_PREPARING, got %v", ord.InternalState())
	}
}

func TestOrderTracker_Update_IFDPromotion(t *testing.T) {
	ot := sniper.NewOrderTracker(nil)
	sniperID := "test-sniper"
	sym := symbol.Symbol{Code: "7203"}

	// Parent order with IFD child
	parent := order.NewOrder("parent-1", "7203", order.ACTION_BUY, 2000, 100)
	parent.BypassTransition(order.ORDER_STATUS_FILLED, order.STATE_CLOSED)

	child := order.NewOrder("child-temp", "7203", order.ACTION_SELL, 2010, 100)
	child.BypassTransition(order.ORDER_STATUS_WAITING, order.STATE_PENDING) // pending state
	parent.IfDone = child

	ot.Add(sniperID, parent)

	// API Report with untracked child order (ParentOrderID = parent-1)
	untrackedChild := order.NewOrder("api-child-1", "7203", order.ACTION_SELL, 2010, 100)
	untrackedChild.ParentOrderID = "parent-1"
	untrackedChild.BypassTransition(order.ORDER_STATUS_IN_PROGRESS, order.STATE_ACTIVE)

	report := order.Orders{
		Orders: []order.Order{*untrackedChild},
	}

	ot.Update(report, sym, time.Now(), func(sID string, exec order.Execution, act order.Action, createdAt time.Time, parentOrder *order.Order) {})

	// Verification
	active := ot.GetActive(sniperID)
	// The parent order should be pruned as it is completed.
	// The untracked child should be promoted and added to active orders.
	if len(active) != 1 {
		t.Fatalf("expected 1 active order (the promoted child), got %d", len(active))
	}
	if active[0].ID != "api-child-1" {
		t.Errorf("expected promoted child ID to be api-child-1, got %s", active[0].ID)
	}
	if parent.IfDone != nil {
		t.Errorf("expected parent's IfDone to be nil after full promotion, got %+v", parent.IfDone)
	}
}

func TestOrderTracker_Update_TombstoneResurrection(t *testing.T) {
	// Path A: Tombstone order matches an ID directly present in the report
	t.Run("Resurrection Path A", func(t *testing.T) {
		ot := sniper.NewOrderTracker(nil)
		sniperID := "test-sniper"
		sym := symbol.Symbol{Code: "7203"}

		o := order.NewOrder("server-id-1", "7203", order.ACTION_BUY, 2000, 100)
		o.BypassTransition(order.ORDER_STATUS_IN_PROGRESS, order.STATE_ACTIVE)
		ot.Add(sniperID, o)
		ot.FailOrder(sniperID, o) // Moves to tombstone

		// Report contains the tombstone's ID (with same active state)
		reportOrd := order.NewOrder("server-id-1", "7203", order.ACTION_BUY, 2000, 100)
		reportOrd.BypassTransition(order.ORDER_STATUS_IN_PROGRESS, order.STATE_ACTIVE)

		report := order.Orders{
			Orders: []order.Order{*reportOrd},
		}

		ot.Update(report, sym, time.Now(), func(sID string, exec order.Execution, act order.Action, createdAt time.Time, parentOrder *order.Order) {})

		// Verification
		active := ot.GetActive(sniperID)
		if len(active) != 1 || active[0].ID != "server-id-1" {
			t.Errorf("expected tombstone to be resurrected to active orders, got %v", active)
		}
	})

	// Path B: Tombstone local ID matches untracked API order properties
	t.Run("Resurrection Path B", func(t *testing.T) {
		ot := sniper.NewOrderTracker(nil)
		sniperID := "test-sniper"
		sym := symbol.Symbol{Code: "7203"}

		o := order.NewOrder("local-temp-id", "7203", order.ACTION_BUY, 2000, 100)
		ot.Add(sniperID, o)
		ot.FailOrder(sniperID, o) // Moves to tombstone

		// Report contains an untracked API order matching properties
		untracked := order.NewOrder("api-resolved-id", "7203", order.ACTION_BUY, 2000, 100)
		untracked.BypassTransition(order.ORDER_STATUS_IN_PROGRESS, order.STATE_ACTIVE)

		report := order.Orders{
			Orders: []order.Order{*untracked},
		}

		ot.Update(report, sym, time.Now(), func(sID string, exec order.Execution, act order.Action, createdAt time.Time, parentOrder *order.Order) {})

		// Verification
		active := ot.GetActive(sniperID)
		if len(active) != 1 {
			t.Fatalf("expected 1 resurrected active order, got %d", len(active))
		}
		if active[0].ID != "api-resolved-id" {
			t.Errorf("expected resurrected order ID to be updated to api-resolved-id, got %s", active[0].ID)
		}
	})
}

type mockPolicy struct {
	strategy.NoopPolicy
	applied []*order.Order
}

func (m *mockPolicy) ApplySyntheticFill(o *order.Order, t tick.Tick) {
	m.applied = append(m.applied, o)
}

func TestOrderTracker_PrepareActiveOrders_Full(t *testing.T) {
	ot := sniper.NewOrderTracker(nil)
	sniperID := "test-sniper"

	// 1. Completed parent order with IfDone child (not preparing)
	p1 := order.NewOrder("p1", "7203", order.ACTION_BUY, 2000, 100)
	p1.BypassTransition(order.ORDER_STATUS_FILLED, order.STATE_CLOSED)
	c1 := order.NewOrder("c1", "7203", order.ACTION_SELL, 2010, 100)
	c1.BypassTransition(order.ORDER_STATUS_WAITING, order.STATE_PENDING)
	p1.IfDone = c1

	// 2. Completed parent order with IfDone child (preparing)
	p2 := order.NewOrder("p2", "7203", order.ACTION_BUY, 2000, 100)
	p2.BypassTransition(order.ORDER_STATUS_FILLED, order.STATE_CLOSED)
	c2 := order.NewOrder("c2", "7203", order.ACTION_SELL, 2010, 100)
	c2.BypassTransition(order.ORDER_STATUS_WAITING, order.STATE_PREPARING)
	p2.IfDone = c2

	// 3. Outstanding active order
	oActive := order.NewOrder("active-1", "7203", order.ACTION_BUY, 2000, 100)
	oActive.BypassTransition(order.ORDER_STATUS_IN_PROGRESS, order.STATE_ACTIVE)

	ot.Add(sniperID, p1)
	ot.Add(sniperID, p2)
	ot.Add(sniperID, oActive)

	policy := &mockPolicy{}
	active, hasProcessing, blocking := ot.PrepareActiveOrders(sniperID, tick.Tick{Price: 2000}, policy)

	// Verification
	// - p1 is completed & filled, its child c1 (pending) is promoted and p1 is discarded.
	// - p2 is completed & filled, but its child c2 is preparing, so p2 is kept.
	// - oActive is outstanding active.
	// Thus, active should contain: c1, p2, oActive.
	if len(active) != 3 {
		t.Fatalf("expected 3 active orders, got %d", len(active))
	}

	// Confirm promotion
	foundC1 := false
	foundP2 := false
	foundActive := false
	for _, o := range active {
		if o.ID == "c1" {
			foundC1 = true
		}
		if o.ID == "p2" {
			foundP2 = true
		}
		if o.ID == "active-1" {
			foundActive = true
		}
	}
	if !foundC1 || !foundP2 || !foundActive {
		t.Errorf("unexpected active orders list: %+v", active)
	}

	// hasProcessing should be true because outstanding active and promoted children exist
	if !hasProcessing {
		t.Error("expected hasProcessing to be true")
	}

	// blockingOrder should be oActive (it is not preparing, not completed, active)
	if blocking != oActive {
		t.Errorf("expected blocking order to be active-1, got %v", blocking)
	}

	// ApplySyntheticFill should be called only on oActive (since c1 is promoted within the loop, the loop continues and c1 doesn't go through ApplySyntheticFill in the same call)
	if len(policy.applied) != 1 {
		t.Errorf("expected ApplySyntheticFill to be called once, got %d times", len(policy.applied))
	}
}

