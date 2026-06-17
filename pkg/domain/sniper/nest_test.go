package sniper

import (
	"log/slog"
	"testing"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/position"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

type mockNestStrategy struct {
	evaluateFn func(input strategy.StrategyInput) strategy.TargetPosition
}

func (m *mockNestStrategy) Name() string { return "nest-mock" }
func (m *mockNestStrategy) Evaluate(input strategy.StrategyInput) strategy.TargetPosition {
	if m.evaluateFn != nil {
		return m.evaluateFn(input)
	}
	return strategy.TargetPosition{}
}
func (m *mockNestStrategy) AnalysisLogger() *slog.Logger { return nil }

func TestSniperNest(t *testing.T) {
	sym := symbol.Symbol{Code: "7203", Name: "Toyota"}

	strat := &mockNestStrategy{}
	s1 := NewSniper("sniper-1", sym, strat, &strategy.NoopPolicy{}, order.EXCHANGE_TOSHO, nil)
	s2 := NewSniper("sniper-2", sym, strat, &strategy.NoopPolicy{}, order.EXCHANGE_SOR, nil)

	nest := NewSniperNest("7203", sym, []*Sniper{s1, s2}, nil)

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
	strat.evaluateFn = func(input strategy.StrategyInput) strategy.TargetPosition {
		return strategy.TargetPosition{
			Qty:       10,
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
		t.Errorf("expected 2 active orders in nest, got %d", len(activeOrders))
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
	nest.AddOrder("sniper-1", newOrd)
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
}

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

func TestSniperNest_PnLAndPerformance(t *testing.T) {
	sym := symbol.Symbol{Code: "7203"}
	nest := NewSniperNest("7203", sym, nil, nil)
	sniperID := "sniper-1"

	// Add positions
	nest.positions.positions[sniperID] = []position.Position{
		{LeavesQty: 10, Price: 2000, Action: order.ACTION_BUY},
		{LeavesQty: 5, Price: 2010, Action: order.ACTION_SELL},
	}

	// 1. GetUnrealizedPnL
	// Buy position: (2020 - 2000) * 10 = 200
	// Sell position: (2020 - 2010) * 5 * -1 = -50
	// Total: 150
	pnl := nest.GetUnrealizedPnL(sniperID, 2020)
	if pnl != 150.0 {
		t.Errorf("expected UnrealizedPnL 150.0, got %f", pnl)
	}

	// 2. GetPerformance / HoldQty
	if nest.HoldQty(sniperID) != 5 {
		t.Errorf("expected HoldQty 5, got %f", nest.HoldQty(sniperID))
	}
	perf := nest.GetPerformance(sniperID)
	if perf.Trades != 0 {
		t.Errorf("expected 0 trades initially, got %d", perf.Trades)
	}
}

func TestSniperNest_RevertOrderStatus(t *testing.T) {
	sym := symbol.Symbol{Code: "7203"}
	nest := NewSniperNest("7203", sym, nil, nil)
	sniperID := "sniper-1"

	ord := order.NewOrder("order-1", "7203", order.ACTION_BUY, 2000, 100)
	nest.AddOrder(sniperID, ord)

	nest.RevertOrderStatus(sniperID, ord, order.ORDER_STATUS_CANCEL_SENT)
	if ord.Status() != order.ORDER_STATUS_CANCEL_SENT {
		t.Errorf("expected status reverted to CANCEL_SENT, got %v", ord.Status())
	}
}

func TestSniperNest_PrepareObservation_IfDoneChild(t *testing.T) {
	sym := symbol.Symbol{Code: "7203"}
	nest := NewSniperNest("7203", sym, nil, nil)
	sniperID := "sniper-1"

	childOrd := order.NewOrder("child", "7203", order.ACTION_SELL, 2100, 100)
	parentOrd := order.NewOrder("parent", "7203", order.ACTION_BUY, 2000, 100)
	parentOrd.IfDone = childOrd
	parentOrd.BypassTransition(order.ORDER_STATUS_FILLED, order.STATE_CLOSED)

	nest.AddOrder(sniperID, parentOrd)

	// 1. When the child order is still in STATE_PREPARING (not yet placed by infrastructure),
	// PrepareObservation should keep the parent order active to block further actions.
	obs := nest.PrepareObservation(sniperID, tick.Tick{Price: 2000}, nil)
	if len(obs.ActiveOrders) != 1 || obs.ActiveOrders[0].ID != "parent" {
		t.Errorf("expected parent order to remain active while child is preparing, got: %v", obs.ActiveOrders)
	}
	if !obs.HasProcessingTrade {
		t.Error("expected HasProcessingTrade to be true")
	}

	// 2. Once the child order transitions to STATE_ACTIVE (placement confirmed),
	// PrepareObservation should extract the child order and remove the parent order.
	childOrd.BypassTransition(order.ORDER_STATUS_WAITING, order.STATE_ACTIVE)
	obs = nest.PrepareObservation(sniperID, tick.Tick{Price: 2000}, nil)
	if len(obs.ActiveOrders) != 1 || obs.ActiveOrders[0].ID != "child" {
		t.Errorf("expected child order to be extracted once active, got: %v", obs.ActiveOrders)
	}
	if !obs.HasProcessingTrade {
		t.Error("expected HasProcessingTrade to be true")
	}
}

func TestSniperNest_ApplyExecution_DuplicateAndRequestFallbacks(t *testing.T) {
	sym := symbol.Symbol{Code: "7203"}
	nest := NewSniperNest("7203", sym, nil, nil)
	sniperID := "sniper-1"

	exec := order.Execution{
		ID:            "exec-1",
		Price:         2000,
		Qty:           100,
		ExecutionTime: time.Now(),
	}

	// 1. First execution (no parentOrder)
	nest.applyExecution(sniperID, exec, order.ACTION_BUY, nil)
	if len(nest.positions.positions[sniperID]) != 1 {
		t.Fatalf("expected 1 position, got %d", len(nest.positions.positions[sniperID]))
	}

	// 2. Duplicate execution (should be ignored)
	nest.applyExecution(sniperID, exec, order.ACTION_BUY, nil)
	if len(nest.positions.positions[sniperID]) != 1 {
		t.Errorf("expected duplicate execution to be ignored, got positions: %d", len(nest.positions.positions[sniperID]))
	}
}

func TestSniperNest_ReducePositions_SpecifiedClose(t *testing.T) {
	sym := symbol.Symbol{Code: "7203"}
	nest := NewSniperNest("7203", sym, nil, nil)
	sniperID := "sniper-1"

	// Setup positions
	nest.positions.positions[sniperID] = []position.Position{
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

	nest.positions.ApplyExecution(sniperID, nest.Detail.Code, exec, order.ACTION_SELL, parentOrder, func(pnl float64) {
		nest.performance.RecordPnL(sniperID, pnl)
	})

	// exec-2 fully closed (deleted), exec-1 has 30 remaining
	remaining := nest.positions.positions[sniperID]
	if len(remaining) != 1 || remaining[0].ExecutionID != "exec-1" || remaining[0].LeavesQty != 30 {
		t.Errorf("unexpected remaining positions: %+v", remaining)
	}

	// Performance verification
	// PnL exec-2: (2020 - 2010) * 50 = 500
	// PnL exec-1: (2020 - 2000) * 70 = 1400
	// Total: 1900
	perf := nest.GetPerformance(sniperID)
	if perf.RealizedPnL != 1900 || perf.Trades != 2 || perf.Wins != 2 {
		t.Errorf("unexpected performance: %+v", perf)
	}
}

func TestSniperNest_ReducePositions_FIFOAndLossPerformance(t *testing.T) {
	sym := symbol.Symbol{Code: "7203"}
	nest := NewSniperNest("7203", sym, nil, nil)
	sniperID := "sniper-1"

	// Setup positions (exec-1 is older, exec-2 is newer)
	nest.positions.positions[sniperID] = []position.Position{
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

	nest.positions.ApplyExecution(sniperID, nest.Detail.Code, exec, order.ACTION_SELL, parentOrder, func(pnl float64) {
		nest.performance.RecordPnL(sniperID, pnl)
	})

	// Remaining: exec-2 with 30 qty
	remaining := nest.positions.positions[sniperID]
	if len(remaining) != 1 || remaining[0].ExecutionID != "exec-2" || remaining[0].LeavesQty != 30 {
		t.Errorf("unexpected remaining positions: %+v", remaining)
	}

	// Performance verification
	// PnL exec-1: (1990 - 2000) * 100 = -1000
	// PnL exec-2: (1990 - 2010) * 20 = -400
	// Total realized: -1400 (2 losses)
	perf := nest.GetPerformance(sniperID)
	if perf.RealizedPnL != -1400 || perf.Trades != 2 || perf.Losses != 2 {
		t.Errorf("unexpected performance: %+v", perf)
	}
}

func TestSniperNest_ReducePositions_FlatPnL(t *testing.T) {
	sym := symbol.Symbol{Code: "7203"}
	nest := NewSniperNest("7203", sym, nil, nil)
	sniperID := "sniper-1"

	nest.positions.positions[sniperID] = []position.Position{
		{ExecutionID: "exec-1", Symbol: "7203", Price: 2000, LeavesQty: 100, Action: order.ACTION_BUY},
	}

	exec := order.Execution{
		ID:    "exec-exit-1",
		Price: 2000, // flat
		Qty:   100,
	}

	parentOrder := order.NewOrder("exit-order", "7203", order.ACTION_SELL, 2000, 100)
	parentOrder.CashMargin = order.CASH_MARGIN_MARGIN_EXIT

	nest.positions.ApplyExecution(sniperID, nest.Detail.Code, exec, order.ACTION_SELL, parentOrder, func(pnl float64) {
		nest.performance.RecordPnL(sniperID, pnl)
	})

	perf := nest.GetPerformance(sniperID)
	if perf.RealizedPnL != 0.0 || perf.Trades != 1 || perf.Wins != 0 || perf.Losses != 0 {
		t.Errorf("unexpected performance: %+v", perf)
	}
}

func TestSniperNest_TombstoneAndResurrection(t *testing.T) {
	sym := symbol.Symbol{Code: "7203"}
	nest := NewSniperNest("7203", sym, nil, nil)
	sniperID := "sniper-1"

	// 1. アクティブ注文の作成と登録
	ord := order.NewOrder("local-123", "7203", order.ACTION_BUY, 2000, 100)
	nest.AddOrder(sniperID, ord)

	// 2. 送信失敗 (FailSendingOrder) により、墓標（Tombstone）に退避させる
	nest.FailSendingOrder(sniperID, ord)

	// アクティブリストからは消えていることを確認
	if len(nest.GetSniperActiveOrders(sniperID)) != 0 {
		t.Fatal("expected 0 active orders after fail sending")
	}
	if len(nest.orders.tombstones[sniperID]) != 1 {
		t.Fatal("expected 1 order in tombstones")
	}

	// 3. 取引所から異なる確定ID（bt_order_99）で注文が返ってきたとシミュレート
	reportOrd := order.NewOrder("bt_order_99", "7203", order.ACTION_BUY, 2000, 100)
	reportOrd.BypassTransition(order.ORDER_STATUS_IN_PROGRESS, order.STATE_ACTIVE)

	report := order.Orders{
		Orders: []order.Order{*reportOrd},
	}

	// Updateを走らせる
	nest.Update(report, time.Now())

	// 4. 復活 (Resurrection) の検証
	// アクティブ注文リストに復活し、IDが bt_order_99 に更新されているはず
	activeOrders := nest.GetSniperActiveOrders(sniperID)
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
	if len(nest.orders.tombstones[sniperID]) != 0 {
		t.Errorf("expected tombstone list to be cleared after resurrection, got %d", len(nest.orders.tombstones[sniperID]))
	}
}

func TestSniperNest_TombstoneExpiration(t *testing.T) {
	sym := symbol.Symbol{Code: "7203"}
	nest := NewSniperNest("7203", sym, nil, nil)
	sniperID := "sniper-1"

	ord := order.NewOrder("local-123", "7203", order.ACTION_BUY, 2000, 100)
	nest.AddOrder(sniperID, ord)

	// 送信失敗により墓標へ退避
	nest.FailSendingOrder(sniperID, ord)

	// 31秒進んだ未来の時刻でUpdateを走らせる (期限は30秒)
	futureTime := time.Now().Add(31 * time.Second)
	report := order.Orders{
		Orders: []order.Order{},
	}
	nest.Update(report, futureTime)

	// 墓標から完全に消え去っていることを確認
	if len(nest.orders.tombstones[sniperID]) != 0 {
		t.Errorf("expected tombstone to expire and be cleared, but got %d", len(nest.orders.tombstones[sniperID]))
	}
	if len(nest.GetSniperActiveOrders(sniperID)) != 0 {
		t.Errorf("expected active orders to remain 0, got %d", len(nest.GetSniperActiveOrders(sniperID)))
	}
}

func TestSniperNest_ActiveOrderFuzzyMatching_PartialFill(t *testing.T) {
	sym := symbol.Symbol{Code: "7203"}
	nest := NewSniperNest("7203", sym, nil, nil)
	sniperID := "sniper-1"

	// 1. Create a parent order that has an IfDone child template of 100 qty in STATE_PREPARING
	childOrd := order.NewOrder("local-child-123", "7203", order.ACTION_SELL, 2100, 100)
	parentOrd := order.NewOrder("parent-123", "7203", order.ACTION_BUY, 2000, 100)
	parentOrd.IfDone = childOrd

	nest.AddOrder(sniperID, parentOrd)

	// 2. Simulate a partial execution child order of 40 qty reported by the broker (API untracked)
	reportOrd1 := order.NewOrder("broker-child-40", "7203", order.ACTION_SELL, 2100, 40)
	reportOrd1.BypassTransition(order.ORDER_STATUS_IN_PROGRESS, order.STATE_ACTIVE)
	reportOrd1.ParentOrderID = "parent-123"

	report1 := order.Orders{
		Orders: []order.Order{*reportOrd1},
	}

	// 3. First update: this should match the nested o.IfDone and split it
	nest.Update(report1, time.Now())

	// Verified: parentOrd is still active, child template qty is reduced to 60, and a new active order of 40 qty is added.
	activeOrders := nest.GetSniperActiveOrders(sniperID)
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
	nest.Update(report2, time.Now())

	activeOrders2 := nest.GetSniperActiveOrders(sniperID)
	// Expect 3 active orders: parentOrd, matchedChild-40, matchedChild-60
	if len(activeOrders2) != 3 {
		t.Fatalf("expected 3 active orders, got %d", len(activeOrders2))
	}

	if parentOrd.IfDone != nil {
		t.Errorf("expected parentOrd.IfDone to be nil after fully consumed, got: %+v", parentOrd.IfDone)
	}
}

type mockCancelCheckerStrategy struct {
	mockNestStrategy
	shouldCancel bool
}

func (m *mockCancelCheckerStrategy) ShouldCancel(input strategy.StrategyInput, ord *order.Order) bool {
	return m.shouldCancel
}

func TestSniperNest_FailSendingOrder_Cooldown(t *testing.T) {
	sym := symbol.Symbol{Code: "7203"}
	nest := NewSniperNest("7203", sym, nil, nil)
	sniperID := "sniper-1"

	// 1. Fail exit order: should trigger cooldown
	exitOrd := order.NewOrder("exit-1", "7203", order.ACTION_SELL, 2000, 100)
	exitOrd.CashMargin = order.CASH_MARGIN_MARGIN_EXIT
	nest.AddOrder(sniperID, exitOrd)

	// lastTickTime is zero
	nest.FailSendingOrder(sniperID, exitOrd)
	if !nest.cooldowns.IsCoolingDown(sniperID, time.Now()) {
		t.Error("expected cooldown to be triggered for exit order failure")
	}

	// 2. Fail exit order with lastTickTime non-zero
	nest2 := NewSniperNest("7203", sym, nil, nil)
	nest2.lastTickTime = time.Now().Add(-10 * time.Second)
	exitOrd2 := order.NewOrder("exit-2", "7203", order.ACTION_SELL, 2000, 100)
	exitOrd2.CashMargin = order.CASH_MARGIN_MARGIN_EXIT
	nest2.AddOrder(sniperID, exitOrd2)
	nest2.FailSendingOrder(sniperID, exitOrd2)
	// Cooldown was triggered 10 seconds ago, so it should NOT be cooling down now
	if nest2.cooldowns.IsCoolingDown(sniperID, time.Now()) {
		t.Error("expected cooldown to be expired since lastTickTime was 10s ago")
	}
}

func TestSniperNest_ReportContainsID_Logic(t *testing.T) {
	sym := symbol.Symbol{Code: "7203"}
	nest := NewSniperNest("7203", sym, nil, nil)
	sniperID := "sniper-1"

	// Parent order (filled)
	parentOrd := order.NewOrder("parent-1", "7203", order.ACTION_BUY, 2000, 100)
	parentOrd.BypassTransition(order.ORDER_STATUS_FILLED, order.STATE_CLOSED)

	// Child order (pending, has ParentOrderID)
	childOrd := order.NewOrder("child-1", "7203", order.ACTION_SELL, 2100, 100)
	childOrd.ParentOrderID = "parent-1"
	childOrd.BypassTransition(order.ORDER_STATUS_WAITING, order.STATE_PENDING)

	nest.AddOrder(sniperID, childOrd)

	// Report contains parent-1, but does NOT contain child-1
	report := order.Orders{
		Orders: []order.Order{*parentOrd},
	}

	nest.Update(report, time.Now())

	// Child order should be preserved in active orders
	active := nest.GetSniperActiveOrders(sniperID)
	if len(active) != 1 || active[0].ID != "child-1" {
		t.Errorf("expected child order to be preserved, active orders: %v", active)
	}

	// Now report contains both parent-1 and child-1 (both parentContains and childContains are true)
	// It should transition the child ord status to whatever status child-1 has in the report
	reportChild := order.NewOrder("child-1", "7203", order.ACTION_SELL, 2100, 100)
	reportChild.BypassTransition(order.ORDER_STATUS_IN_PROGRESS, order.STATE_ACTIVE)
	report2 := order.Orders{
		Orders: []order.Order{*parentOrd, *reportChild},
	}
	nest.Update(report2, time.Now())
	active2 := nest.GetSniperActiveOrders(sniperID)
	if len(active2) != 1 || active2[0].Status() != order.ORDER_STATUS_IN_PROGRESS {
		t.Errorf("expected child order to transition, got status: %v", active2[0].Status())
	}
}

func TestSniperNest_Update_WithExecutions(t *testing.T) {
	sym := symbol.Symbol{Code: "7203"}
	nest := NewSniperNest("7203", sym, nil, nil)
	sniperID := "sniper-1"

	ord := order.NewOrder("order-1", "7203", order.ACTION_BUY, 2000, 100)
	ord.BypassTransition(order.ORDER_STATUS_IN_PROGRESS, order.STATE_ACTIVE)
	nest.AddOrder(sniperID, ord)

	// 1. Report contains BUY execution
	reportOrd := order.NewOrder("order-1", "7203", order.ACTION_BUY, 2000, 100)
	reportOrd.BypassTransition(order.ORDER_STATUS_IN_PROGRESS, order.STATE_ACTIVE)
	reportOrd.Executions = []order.Execution{
		{ID: "exec-new-1", Price: 2000, Qty: 50, ExecutionTime: time.Now()},
	}

	report := order.Orders{
		Orders: []order.Order{*reportOrd},
	}

	nest.Update(report, time.Now())

	// Position should be updated
	if nest.HoldQty(sniperID) != 50 {
		t.Errorf("expected HoldQty 50, got %f", nest.HoldQty(sniperID))
	}

	// 2. Add an exit SELL order
	exitOrd := order.NewOrder("order-2", "7203", order.ACTION_SELL, 2010, 50)
	exitOrd.CashMargin = order.CASH_MARGIN_MARGIN_EXIT
	exitOrd.BypassTransition(order.ORDER_STATUS_IN_PROGRESS, order.STATE_ACTIVE)
	nest.AddOrder(sniperID, exitOrd)

	// Report contains SELL execution (reduces position, realizes PnL)
	reportOrd2 := order.NewOrder("order-2", "7203", order.ACTION_SELL, 2010, 50)
	reportOrd2.CashMargin = order.CASH_MARGIN_MARGIN_EXIT
	reportOrd2.BypassTransition(order.ORDER_STATUS_IN_PROGRESS, order.STATE_ACTIVE)
	reportOrd2.Executions = []order.Execution{
		{ID: "exec-exit-new", Price: 2010, Qty: 50, ExecutionTime: time.Now()},
	}

	report2 := order.Orders{
		Orders: []order.Order{*reportOrd2},
	}

	nest.Update(report2, time.Now())

	// Position should be flat
	if nest.HoldQty(sniperID) != 0 {
		t.Errorf("expected HoldQty 0, got %f", nest.HoldQty(sniperID))
	}

	// Performance should show PnL: (2010 - 2000) * 50 = 500
	perf := nest.GetPerformance(sniperID)
	if perf.RealizedPnL != 500 {
		t.Errorf("expected RealizedPnL 500, got %f", perf.RealizedPnL)
	}
}

func TestSniperNest_ReconcileTarget_MoreBranches(t *testing.T) {
	sym := symbol.Symbol{Code: "7203"}
	nest := NewSniperNest("7203", sym, nil, nil)
	sniperID := "sniper-1"

	// 1. Target is negative (-50, short entry)
	targetShort := strategy.TargetPosition{Qty: -50, Price: 2000, OrderType: order.ORDER_TYPE_LIMIT}
	bullet := nest.ReconcileTarget(sniperID, tick.Tick{Price: 2000}, strategy.Position{Qty: 0}, targetShort, order.EXCHANGE_TOSHO, order.TRADE_TYPE_SYSTEM, order.ACCOUNT_SPECIAL, &strategy.NoopPolicy{})
	orderBullet, ok := bullet.(OrderBullet)
	if !ok {
		t.Fatalf("expected OrderBullet, got %+v", bullet)
	}
	if orderBullet.Order.Action != order.ACTION_SELL || orderBullet.Order.CashMargin != order.CASH_MARGIN_MARGIN_ENTRY {
		t.Errorf("expected new sell entry order for short position, got %+v", orderBullet.Order)
	}

	// 2. Virtual Qty is negative (-100), Target is 0 -> should BUY exit
	nest2 := NewSniperNest("7203", sym, nil, nil)
	nest2.positions.positions[sniperID] = []position.Position{
		{LeavesQty: 100, Price: 2000, Action: order.ACTION_SELL},
	}
	targetZero := strategy.TargetPosition{Qty: 0, Price: 0, OrderType: order.ORDER_TYPE_MARKET}
	bullet2 := nest2.ReconcileTarget(sniperID, tick.Tick{Price: 2000}, strategy.Position{Qty: -100, AveragePrice: 2000}, targetZero, order.EXCHANGE_TOSHO, order.TRADE_TYPE_SYSTEM, order.ACCOUNT_SPECIAL, &strategy.NoopPolicy{})
	orderBullet2, ok2 := bullet2.(OrderBullet)
	if !ok2 {
		t.Fatalf("expected OrderBullet, got %+v", bullet2)
	}
	if orderBullet2.Order.Action != order.ACTION_BUY || orderBullet2.Order.CashMargin != order.CASH_MARGIN_MARGIN_EXIT {
		t.Errorf("expected buy exit order, got %+v", orderBullet2.Order)
	}

	// 3. Invalid order cancel check: effectiveTargetQty < 0 (short), but active order is BUY entry
	nest3 := NewSniperNest("7203", sym, nil, nil)
	ordBuy := order.NewOrder("buy-entry-ord", "7203", order.ACTION_BUY, 2000, 100)
	ordBuy.BypassTransition(order.ORDER_STATUS_IN_PROGRESS, order.STATE_ACTIVE)
	nest3.AddOrder(sniperID, ordBuy)

	bullet3 := nest3.ReconcileTarget(sniperID, tick.Tick{Price: 2000}, strategy.Position{Qty: 0}, targetShort, order.EXCHANGE_TOSHO, order.TRADE_TYPE_SYSTEM, order.ACCOUNT_SPECIAL, &strategy.NoopPolicy{})
	cancelBullet3, ok3 := bullet3.(CancelBullet)
	if !ok3 || cancelBullet3.OrderID != "buy-entry-ord" {
		t.Errorf("expected CancelBullet for buy-entry-ord, got %+v", bullet3)
	}
}


func TestSniperNest_ReconcileTarget_CancelCheck(t *testing.T) {
	sym := symbol.Symbol{Code: "7203"}
	strat := &mockCancelCheckerStrategy{
		shouldCancel: true,
	}
	s1 := NewSniper("sniper-1", sym, strat, &strategy.NoopPolicy{}, order.EXCHANGE_TOSHO, nil)
	nest := NewSniperNest("7203", sym, []*Sniper{s1}, nil)

	// Setup position and active order
	nest.positions.positions["sniper-1"] = []position.Position{
		{LeavesQty: 100, Price: 2000, Action: order.ACTION_BUY},
	}
	ord := order.NewOrder("order-1", "7203", order.ACTION_BUY, 2000, 100)
	ord.BypassTransition(order.ORDER_STATUS_IN_PROGRESS, order.STATE_ACTIVE)
	nest.AddOrder("sniper-1", ord)

	target := strategy.TargetPosition{Qty: 100, Price: 2000, OrderType: order.ORDER_TYPE_LIMIT}
	bullet := nest.ReconcileTarget("sniper-1", tick.Tick{Price: 2000}, strategy.Position{Qty: 200, AveragePrice: 2000}, target, order.EXCHANGE_TOSHO, order.TRADE_TYPE_SYSTEM, order.ACCOUNT_SPECIAL, &strategy.NoopPolicy{})

	cancelBullet, ok := bullet.(CancelBullet)
	if !ok || cancelBullet.OrderID != "order-1" {
		t.Errorf("expected CancelBullet for order-1, got %+v", bullet)
	}
}

func TestSniperNest_ReconcileTarget_ExitCooldown(t *testing.T) {
	sym := symbol.Symbol{Code: "7203"}
	nest := NewSniperNest("7203", sym, nil, nil)
	sniperID := "sniper-1"

	// Position exists
	nest.positions.positions[sniperID] = []position.Position{
		{LeavesQty: 100, Price: 2000, Action: order.ACTION_BUY},
	}

	// Trigger cooldown
	nest.cooldowns.Trigger(sniperID)

	// Target is 0 (exit)
	target := strategy.TargetPosition{Qty: 0, Price: 0, OrderType: order.ORDER_TYPE_MARKET}
	bullet := nest.ReconcileTarget(sniperID, tick.Tick{Price: 2000}, strategy.Position{Qty: 100, AveragePrice: 2000}, target, order.EXCHANGE_TOSHO, order.TRADE_TYPE_SYSTEM, order.ACCOUNT_SPECIAL, &strategy.NoopPolicy{})

	if bullet != nil {
		t.Errorf("expected ReconcileTarget to return nil due to active cooldown, got %+v", bullet)
	}
}

func TestSniperNest_ReconcileTarget_PosInversion(t *testing.T) {
	sym := symbol.Symbol{Code: "7203"}
	nest := NewSniperNest("7203", sym, nil, nil)
	sniperID := "sniper-1"

	// Virtual Qty is positive (+100)
	nest.positions.positions[sniperID] = []position.Position{
		{LeavesQty: 100, Price: 2000, Action: order.ACTION_BUY},
	}

	// Target is negative (-50) -> position inversion safety valve should set effectiveTargetQty to 0
	target := strategy.TargetPosition{Qty: -50, Price: 2000, OrderType: order.ORDER_TYPE_LIMIT}
	bullet := nest.ReconcileTarget(sniperID, tick.Tick{Price: 2000}, strategy.Position{Qty: 100, AveragePrice: 2000}, target, order.EXCHANGE_TOSHO, order.TRADE_TYPE_SYSTEM, order.ACCOUNT_SPECIAL, &strategy.NoopPolicy{})

	// Effective target is 0, virtual qty is 100 -> gap is -100 -> exit of 100 units
	orderBullet, ok := bullet.(OrderBullet)
	if !ok {
		t.Fatalf("expected OrderBullet, got %+v", bullet)
	}
	if orderBullet.Order.Action != order.ACTION_SELL || orderBullet.Order.OrderQty != 100 {
		t.Errorf("expected exit sell order of 100 units due to safety valve, got: %+v", orderBullet.Order)
	}

	// Opposite case: Virtual Qty is negative (-100), Target is positive (+50)
	nest2 := NewSniperNest("7203", sym, nil, nil)
	nest2.positions.positions[sniperID] = []position.Position{
		{LeavesQty: 100, Price: 2000, Action: order.ACTION_SELL},
	}
	bullet2 := nest2.ReconcileTarget(sniperID, tick.Tick{Price: 2000}, strategy.Position{Qty: -100, AveragePrice: 2000}, strategy.TargetPosition{Qty: 50, Price: 2000, OrderType: order.ORDER_TYPE_LIMIT}, order.EXCHANGE_TOSHO, order.TRADE_TYPE_SYSTEM, order.ACCOUNT_SPECIAL, &strategy.NoopPolicy{})
	orderBullet2, ok2 := bullet2.(OrderBullet)
	if !ok2 {
		t.Fatalf("expected OrderBullet, got %+v", bullet2)
	}
	if orderBullet2.Order.Action != order.ACTION_BUY || orderBullet2.Order.OrderQty != 100 {
		t.Errorf("expected exit buy order of 100 units due to safety valve, got: %+v", orderBullet2.Order)
	}
}

func TestObservation_CalculateVirtualPosition(t *testing.T) {
	// Active orders contain entry BUY and entry SELL (short) which are FillExpected
	oBuy := order.NewOrder("oBuy", "7203", order.ACTION_BUY, 2000, 100)
	oBuy.BypassTransition(order.ORDER_STATUS_FILL_EXPECTED, order.STATE_ACTIVE)
	oBuy.CashMargin = order.CASH_MARGIN_MARGIN_ENTRY

	oSell := order.NewOrder("oSell", "7203", order.ACTION_SELL, 2000, 40)
	oSell.BypassTransition(order.ORDER_STATUS_FILL_EXPECTED, order.STATE_ACTIVE)
	oSell.CashMargin = order.CASH_MARGIN_MARGIN_ENTRY

	obs := Observation{
		Positions: []position.Position{
			{LeavesQty: 50, Price: 2000, Action: order.ACTION_BUY},
		},
		ActiveOrders: []*order.Order{oBuy, oSell},
	}

	vpos := obs.CalculateVirtualPosition()
	// Physical position: 50
	// Entry Buy: +100
	// Entry Sell: -40
	// Expected total qty: 50 + 100 - 40 = 110
	if vpos.Qty != 110 {
		t.Errorf("expected virtual qty 110, got %f", vpos.Qty)
	}
	expectedPrice := (50*2000.0 + 100*2000.0 - 40*2000.0) / 110.0 // totalCost / totalQty
	if vpos.AveragePrice != expectedPrice {
		t.Errorf("expected avg price %f, got %f", expectedPrice, vpos.AveragePrice)
	}

	// Test case for purely short positions:
	obsShort := Observation{
		Positions: []position.Position{
			{LeavesQty: 100, Price: 2000, Action: order.ACTION_SELL},
		},
		ActiveOrders: []*order.Order{},
	}
	vposShort := obsShort.CalculateVirtualPosition()
	if vposShort.Qty != -100 {
		t.Errorf("expected virtual qty -100, got %f", vposShort.Qty)
	}
	if vposShort.AveragePrice != 2000.0 {
		t.Errorf("expected avg price 2000.0 for short position, got %f", vposShort.AveragePrice)
	}
}

func TestSniperNest_HandleTick_StoppedLifecycle(t *testing.T) {
	sym := symbol.Symbol{Code: "7203", Name: "Toyota"}
	strat := &mockNestStrategy{
		evaluateFn: func(input strategy.StrategyInput) strategy.TargetPosition {
			return strategy.TargetPosition{
				Qty:       10,
				Price:     2000,
				OrderType: order.ORDER_TYPE_LIMIT,
				Reason:    "TestBuy",
			}
		},
	}
	s1 := NewSniper("sniper-1", sym, strat, &strategy.NoopPolicy{}, order.EXCHANGE_TOSHO, nil)
	s1.ForceStop() // LifecycleをStoppedにする

	nest := NewSniperNest("7203", sym, []*Sniper{s1}, nil)

	actions := nest.HandleTick(tick.Tick{Symbol: "7203", Price: 2000, TradingVolume: 100})
	if len(actions) != 0 {
		t.Fatalf("expected 0 fire actions for stopped sniper, got %d", len(actions))
	}
}


