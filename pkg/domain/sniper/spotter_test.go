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

	// 1. When the child order is still in STATE_PREPARING (not yet placed by infrastructure),
	// PrepareObservation should keep the parent order active to block further actions.
	obs := sp.PrepareObservation(sniperID, tick.Tick{Price: 2000}, nil)
	if len(obs.ActiveOrders) != 1 || obs.ActiveOrders[0].ID != "parent" {
		t.Errorf("expected parent order to remain active while child is preparing, got: %v", obs.ActiveOrders)
	}
	if !obs.HasProcessingTrade {
		t.Error("expected HasProcessingTrade to be true")
	}

	// 2. Once the child order transitions to STATE_ACTIVE (placement confirmed),
	// PrepareObservation should extract the child order and remove the parent order.
	childOrd.BypassTransition(order.ORDER_STATUS_WAITING, order.STATE_ACTIVE)
	obs = sp.PrepareObservation(sniperID, tick.Tick{Price: 2000}, nil)
	if len(obs.ActiveOrders) != 1 || obs.ActiveOrders[0].ID != "child" {
		t.Errorf("expected child order to be extracted once active, got: %v", obs.ActiveOrders)
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

func TestSpotter_TombstoneAndResurrection(t *testing.T) {
	sym := symbol.Symbol{Code: "7203"}
	sp := NewSpotter(sym, nil)
	sniperID := "sniper-1"

	// 1. アクティブ注文の作成と登録
	ord := order.NewOrder("local-123", "7203", order.ACTION_BUY, 2000, 100)
	sp.AddOrder(sniperID, ord)

	// 2. 送信失敗 (FailSendingOrder) により、墓標（Tombstone）に退避させる
	sp.FailSendingOrder(sniperID, ord)

	// アクティブリストからは消えていることを確認
	if len(sp.GetSniperActiveOrders(sniperID)) != 0 {
		t.Fatal("expected 0 active orders after fail sending")
	}
	if len(sp.tombstones[sniperID]) != 1 {
		t.Fatal("expected 1 order in tombstones")
	}

	// 3. 取引所から異なる確定ID（bt_order_99）で注文が返ってきたとシミュレート
	reportOrd := order.NewOrder("bt_order_99", "7203", order.ACTION_BUY, 2000, 100)
	reportOrd.BypassTransition(order.ORDER_STATUS_IN_PROGRESS, order.STATE_ACTIVE)

	report := order.Orders{
		Orders: []order.Order{*reportOrd},
	}

	// Updateを走らせる
	sp.Update(report, time.Now())

	// 4. 復活 (Resurrection) の検証
	// アクティブ注文リストに復活し、IDが bt_order_99 に更新されているはず
	activeOrders := sp.GetSniperActiveOrders(sniperID)
	if len(activeOrders) != 1 {
		t.Fatalf("expected 1 active order revived, got %d", len(activeOrders))
	}
	revived := activeOrders[0]
	if revived.ID != "bt_order_99" {
		t.Errorf("expected revived order ID to be updated to 'bt_order_99', got %s", revived.ID)
	}
	if revived.InternalState() != order.STATE_ACTIVE {
		t.Errorf("expected revived internal state to be STATE_ACTIVE, got %v", revived.InternalState())
	}

	// 墓標からは消えていることを確認
	if len(sp.tombstones[sniperID]) != 0 {
		t.Errorf("expected tombstone list to be cleared after resurrection, got %d", len(sp.tombstones[sniperID]))
	}
}

func TestSpotter_TombstoneExpiration(t *testing.T) {
	sym := symbol.Symbol{Code: "7203"}
	sp := NewSpotter(sym, nil)
	sniperID := "sniper-1"

	ord := order.NewOrder("local-123", "7203", order.ACTION_BUY, 2000, 100)
	sp.AddOrder(sniperID, ord)

	// 送信失敗により墓標へ退避
	sp.FailSendingOrder(sniperID, ord)

	// 31秒進んだ未来の時刻でUpdateを走らせる (期限は30秒)
	futureTime := time.Now().Add(31 * time.Second)
	report := order.Orders{
		Orders: []order.Order{},
	}
	sp.Update(report, futureTime)

	// 墓標から完全に消え去っていることを確認
	if len(sp.tombstones[sniperID]) != 0 {
		t.Errorf("expected tombstone to expire and be cleared, but got %d", len(sp.tombstones[sniperID]))
	}
	if len(sp.GetSniperActiveOrders(sniperID)) != 0 {
		t.Errorf("expected active orders to remain 0, got %d", len(sp.GetSniperActiveOrders(sniperID)))
	}
}

func TestSpotter_ActiveOrderFuzzyMatching_PartialFill(t *testing.T) {
	sym := symbol.Symbol{Code: "7203"}
	sp := NewSpotter(sym, nil)
	sniperID := "sniper-1"

	// 1. Create a parent order that has an IfDone child template of 100 qty in STATE_PREPARING
	childOrd := order.NewOrder("local-child-123", "7203", order.ACTION_SELL, 2100, 100)
	parentOrd := order.NewOrder("parent-123", "7203", order.ACTION_BUY, 2000, 100)
	parentOrd.IfDone = childOrd

	sp.AddOrder(sniperID, parentOrd)

	// 2. Simulate a partial execution child order of 40 qty reported by the broker (API untracked)
	reportOrd1 := order.NewOrder("broker-child-40", "7203", order.ACTION_SELL, 2100, 40)
	reportOrd1.BypassTransition(order.ORDER_STATUS_IN_PROGRESS, order.STATE_ACTIVE)
	reportOrd1.ParentOrderID = "parent-123"

	report1 := order.Orders{
		Orders: []order.Order{*reportOrd1},
	}

	// 3. First update: this should match the nested o.IfDone and split it
	sp.Update(report1, time.Now())

	// Verified: parentOrd is still active, child template qty is reduced to 60, and a new active order of 40 qty is added.
	activeOrders := sp.GetSniperActiveOrders(sniperID)
	// Expect 2 active orders: parentOrd, and matchedChild (40 qty)
	if len(activeOrders) != 2 {
		t.Fatalf("expected 2 active orders, got %d", len(activeOrders))
	}

	var matchedChild *order.Order
	for _, o := range activeOrders {
		if o.ID == "broker-child-40" {
			matchedChild = o
		}
	}
	if matchedChild == nil {
		t.Fatal("expected matchedChild to be found in active orders")
	}
	if matchedChild.OrderQty != 40 {
		t.Errorf("expected matchedChild qty to be 40, got %f", matchedChild.OrderQty)
	}
	if parentOrd.IfDone == nil {
		t.Fatal("expected parentOrd.IfDone to still exist")
	}
	if parentOrd.IfDone.OrderQty != 60 {
		t.Errorf("expected parentOrd.IfDone qty to be reduced to 60, got %f", parentOrd.IfDone.OrderQty)
	}

	// 4. Simulate the second child order of 60 qty reported by the broker
	reportOrd2 := order.NewOrder("broker-child-60", "7203", order.ACTION_SELL, 2100, 60)
	reportOrd2.BypassTransition(order.ORDER_STATUS_IN_PROGRESS, order.STATE_ACTIVE)
	reportOrd2.ParentOrderID = "parent-123"

	report2 := order.Orders{
		Orders: []order.Order{*reportOrd1, *reportOrd2},
	}

	// 5. Second update: this should consume the remaining o.IfDone completely
	sp.Update(report2, time.Now())

	activeOrders2 := sp.GetSniperActiveOrders(sniperID)
	// Expect 3 active orders: parentOrd, matchedChild-40, matchedChild-60
	if len(activeOrders2) != 3 {
		t.Fatalf("expected 3 active orders, got %d", len(activeOrders2))
	}

	if parentOrd.IfDone != nil {
		t.Errorf("expected parentOrd.IfDone to be nil after fully consumed, got: %+v", parentOrd.IfDone)
	}
}


