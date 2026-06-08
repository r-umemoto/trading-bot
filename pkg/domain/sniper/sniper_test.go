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

type ControllableStrategy struct {
	name       string
	evaluateFn func(input strategy.StrategyInput) strategy.TargetPosition
}

func (m *ControllableStrategy) Name() string {
	if m.name != "" {
		return m.name
	}
	return "controllable-strategy"
}

func (m *ControllableStrategy) Evaluate(input strategy.StrategyInput) strategy.TargetPosition {
	if m.evaluateFn != nil {
		return m.evaluateFn(input)
	}
	return strategy.TargetPosition{}
}

func (m *ControllableStrategy) AnalysisLogger() *slog.Logger { return nil }

func TestSniper_BasicGettersAndMetadata(t *testing.T) {
	detail := symbol.Symbol{Code: "9434", Name: "Softbank"}
	strat := &ControllableStrategy{name: "test-strat"}
	s := NewSniper("sniper-1", detail, strat, &strategy.NoopPolicy{}, order.EXCHANGE_TOSHO, nil)

	if s.GetID() != "sniper-1" {
		t.Errorf("expected sniper-1, got %s", s.GetID())
	}
	if s.GetSymbolCode() != "9434" {
		t.Errorf("expected 9434, got %s", s.GetSymbolCode())
	}
	if s.GetStrategyName() != "test-strat" {
		t.Errorf("expected test-strat, got %s", s.GetStrategyName())
	}

	// Bullet methods
	ob := OrderBullet{}
	ob.isBullet()
	cb := CancelBullet{}
	cb.isBullet()
}

func testTick(s *Sniper, nest *SniperNest, obs Observation) Bullet {
	input := strategy.StrategyInput{
		Position:   obs.CalculateVirtualPosition(),
		LatestTick: obs.Tick,
	}
	target := s.Evaluate(input)
	return nest.ReconcileTarget(s.ID, obs.Tick, target, s.Exchange, s.MarginTradeType, s.AccountType, s.ExecutionPolicy)
}

func TestSniper_Tick_Lifecycle(t *testing.T) {
	detail := symbol.Symbol{Code: "9434"}
	strat := &ControllableStrategy{}
	s := NewSniper("sniper-1", detail, strat, &strategy.NoopPolicy{}, order.EXCHANGE_TOSHO, nil)
	nest := NewSniperNest("9434", detail, []*Sniper{s}, nil)

	// 1. LifecycleStopped
	s.ForceStop()
	if s.GetLifecycle() != LifecycleStopped {
		t.Errorf("expected LifecycleStopped, got %v", s.GetLifecycle())
	}
	obs := Observation{Tick: tick.Tick{Price: 2000, CurrentPriceTime: time.Now()}}
	if bullet := testTick(s, nest, obs); bullet != nil {
		t.Error("expected nil bullet when stopped")
	}

	// 2. LifecycleExiting with zero hold Qty
	s = NewSniper("sniper-1", detail, strat, &strategy.NoopPolicy{}, order.EXCHANGE_TOSHO, nil)
	s.OrderlyExit()
	if s.GetLifecycle() != LifecycleExiting {
		t.Errorf("expected LifecycleExiting, got %v", s.GetLifecycle())
	}
	nest = NewSniperNest("9434", detail, []*Sniper{s}, nil)
	// Sniper orderly exits, hold Qty = 0 -> should evaluate signal to HOLD (returns nil bullet)
	bullet := testTick(s, nest, obs)
	if bullet != nil {
		t.Errorf("expected nil bullet on exit with zero positions, got %v", bullet)
	}

	// 3. LifecycleExiting with positive hold Qty -> should generate Selling Force Exit order
	obsWithPos := Observation{
		Tick: tick.Tick{Price: 2000, CurrentPriceTime: time.Now()},
		Positions: []position.Position{
			{LeavesQty: 10, Price: 2000, Action: order.ACTION_BUY},
		},
	}
	nest.positions.positions[s.ID] = obsWithPos.Positions
	bullet = testTick(s, nest, obsWithPos)
	if bullet == nil {
		t.Fatal("expected order bullet for force exit")
	}
	ob, ok := bullet.(OrderBullet)
	if !ok {
		t.Fatalf("expected OrderBullet, got %T", bullet)
	}
	if ob.Order.Action != order.ACTION_SELL || ob.Order.OrderQty != 10 || ob.Order.Type != order.ORDER_TYPE_MARKET {
		t.Errorf("unexpected force exit order: %+v", ob.Order)
	}

	// 4. LifecycleExiting with negative hold Qty -> should generate Buying Force Exit order
	obsWithNegPos := Observation{
		Tick: tick.Tick{Price: 2000, CurrentPriceTime: time.Now()},
		Positions: []position.Position{
			{LeavesQty: 5, Price: 2000, Action: order.ACTION_SELL},
		},
	}
	nest.positions.positions[s.ID] = obsWithNegPos.Positions
	bullet = testTick(s, nest, obsWithNegPos)
	if bullet == nil {
		t.Fatal("expected order bullet for force exit")
	}
	ob, ok = bullet.(OrderBullet)
	if !ok {
		t.Fatalf("expected OrderBullet, got %T", bullet)
	}
	if ob.Order.Action != order.ACTION_BUY || ob.Order.OrderQty != 5 || ob.Order.Type != order.ORDER_TYPE_MARKET {
		t.Errorf("unexpected force exit order: %+v", ob.Order)
	}
}

func TestSniper_Tick_CancellationAndCooldown(t *testing.T) {
	detail := symbol.Symbol{Code: "9434"}
	strat := &ControllableStrategy{}
	s := NewSniper("sniper-1", detail, strat, &strategy.NoopPolicy{}, order.EXCHANGE_TOSHO, nil)
	nest := NewSniperNest("9434", detail, []*Sniper{s}, nil)

	now := time.Now()
	ord := order.NewOrder("test-order", "9434", order.ACTION_BUY, 2000, 100)
	ord.BypassTransition(order.ORDER_STATUS_IN_PROGRESS, order.STATE_ACTIVE)

	obs := Observation{
		Tick:         tick.Tick{Price: 2000, CurrentPriceTime: now},
		ActiveOrders: []*order.Order{ord},
	}
	nest.AddOrder(s.ID, ord)

	// 1. Should cancel active order
	strat.evaluateFn = func(input strategy.StrategyInput) strategy.TargetPosition {
		return strategy.TargetPosition{Qty: 0}
	}
	bullet := testTick(s, nest, obs)
	if bullet == nil {
		t.Fatal("expected cancel bullet")
	}
	cb, ok := bullet.(CancelBullet)
	if !ok {
		t.Fatalf("expected CancelBullet, got %T", bullet)
	}
	if cb.OrderID != "test-order" {
		t.Errorf("expected test-order, got %s", cb.OrderID)
	}
	if ord.Status() != order.ORDER_STATUS_CANCEL_SENT || ord.CancelSentAt != now {
		t.Errorf("unexpected canceled order state: %+v", ord)
	}

	nest.cooldowns.TriggerWithTime(s.ID, now)
	strat.evaluateFn = func(input strategy.StrategyInput) strategy.TargetPosition {
		return strategy.TargetPosition{Qty: 0}
	}
	obsWithPos := Observation{
		Tick: tick.Tick{Price: 2000, CurrentPriceTime: now.Add(500 * time.Millisecond)},
		Positions: []position.Position{
			{LeavesQty: 10, Price: 2000, Action: order.ACTION_BUY},
		},
	}
	nest.positions.positions[s.ID] = obsWithPos.Positions
	bullet = testTick(s, nest, obsWithPos)
	if bullet != nil {
		t.Errorf("expected cooldown block, but got bullet: %v", bullet)
	}
}

func TestSniper_Tick_PreparingOrderHandling(t *testing.T) {
	detail := symbol.Symbol{Code: "9434"}
	strat := &ControllableStrategy{}
	s := NewSniper("sniper-1", detail, strat, &strategy.NoopPolicy{}, order.EXCHANGE_TOSHO, nil)
	nest := NewSniperNest("9434", detail, []*Sniper{s}, nil)

	now := time.Now()
	// Preparing order represents order in queue waiting to be sent
	prepOrd := order.NewOrder("prep-order", "9434", order.ACTION_BUY, 2000, 100)

	obs := Observation{
		Tick:         tick.Tick{Price: 2000, CurrentPriceTime: now},
		ActiveOrders: []*order.Order{prepOrd},
	}
	nest.AddOrder(s.ID, prepOrd)

	// 1. Latest target is 0 (returns CancelBullet)
	strat.evaluateFn = func(input strategy.StrategyInput) strategy.TargetPosition {
		return strategy.TargetPosition{Qty: 0}
	}
	bullet := testTick(s, nest, obs)
	if bullet == nil {
		t.Fatal("expected cancel bullet for preparing order when target is 0")
	}
	cb, ok := bullet.(CancelBullet)
	if !ok || cb.OrderID != "prep-order" {
		t.Errorf("expected CancelBullet for prep-order, got %v", bullet)
	}

	// 2. Latest target matches preparing order exactly (returns nil block)
	strat.evaluateFn = func(input strategy.StrategyInput) strategy.TargetPosition {
		return strategy.TargetPosition{
			Qty:       100,
			Price:     2000,
			OrderType: order.ORDER_TYPE_LIMIT,
		}
	}
	bullet = testTick(s, nest, obs)
	if bullet != nil {
		t.Errorf("expected block (nil bullet) when signal matches prep order, got %v", bullet)
	}

	// 3. Latest target differs from prep order -> directly issues new OrderBullet to overwrite
	strat.evaluateFn = func(input strategy.StrategyInput) strategy.TargetPosition {
		return strategy.TargetPosition{
			Qty:       50, // different qty
			Price:     2000,
			OrderType: order.ORDER_TYPE_LIMIT,
		}
	}
	bullet = testTick(s, nest, obs)
	if bullet == nil {
		t.Fatal("expected order bullet to overwrite prep order")
	}
	ob, ok := bullet.(OrderBullet)
	if !ok || ob.Order.OrderQty != 50 {
		t.Errorf("expected overwrite order with qty 50, got %+v", bullet)
	}
}

func TestSniper_BuildOrderPairWithIfDone(t *testing.T) {
	detail := symbol.Symbol{Code: "9434"}
	strat := &ControllableStrategy{}
	s := NewSniper("sniper-1", detail, strat, &strategy.NoopPolicy{}, order.EXCHANGE_TOSHO, nil)
	nest := NewSniperNest("9434", detail, []*Sniper{s}, nil)

	strat.evaluateFn = func(input strategy.StrategyInput) strategy.TargetPosition {
		return strategy.TargetPosition{
			Qty:           10,
			Price:         2000,
			OrderType:     order.ORDER_TYPE_LIMIT,
			HasIfDone:     true,
			ExitPrice:     2100,
			ExitOrderType: order.ORDER_TYPE_LIMIT,
			ExitReason:    "ProfitTake",
		}
	}

	obs := Observation{
		Tick: tick.Tick{Price: 2000, CurrentPriceTime: time.Now()},
	}

	bullet := testTick(s, nest, obs)
	if bullet == nil {
		t.Fatal("expected order pair bullet")
	}
	ob, ok := bullet.(OrderBullet)
	if !ok {
		t.Fatalf("expected OrderBullet, got %T", bullet)
	}

	entry := ob.Order
	if entry.IfDone == nil {
		t.Fatal("expected IfDone order not to be nil")
	}
	exit := entry.IfDone
	if exit.Action != order.ACTION_SELL || exit.OrderPrice != 2100 || exit.OrderQty != 10 || exit.Reason != "ProfitTake" {
		t.Errorf("unexpected exit order details: %+v", exit)
	}
}

func TestSniper_MatchPositionsToClose(t *testing.T) {
	detail := symbol.Symbol{Code: "9434"}
	nest := NewSniperNest("9434", detail, nil, nil)

	// Positions: Long execution-1 (60 Qty), Long execution-2 (50 Qty)
	positions := []position.Position{
		{ExecutionID: "exec-1", Symbol: "9434", LeavesQty: 60, Price: 2000, Action: order.ACTION_BUY},
		{ExecutionID: "exec-2", Symbol: "9434", LeavesQty: 50, Price: 2010, Action: order.ACTION_BUY},
	}
	nest.positions.positions["test-sniper"] = positions

	// 1. Closes Long positions (exit Sell order matches Buy positions)
	closePositions, _ := nest.positions.MatchPositionsToClose("test-sniper", order.ACTION_SELL, 80, nil)
	if len(closePositions) != 2 {
		t.Fatalf("expected 2 close positions, got %d", len(closePositions))
	}
	// first position should be fully closed (60)
	if closePositions[0].HoldID != "exec-1" || closePositions[0].Qty != 60 {
		t.Errorf("unexpected first close position: %+v", closePositions[0])
	}
	// second position should be partially closed (20)
	if closePositions[1].HoldID != "exec-2" || closePositions[1].Qty != 20 {
		t.Errorf("unexpected second close position: %+v", closePositions[1])
	}

	// 2. Closes Long positions skipping locked execution-1
	locked := map[string]bool{"exec-1": true}
	closePositions, _ = nest.positions.MatchPositionsToClose("test-sniper", order.ACTION_SELL, 80, locked)
	if len(closePositions) != 1 {
		t.Fatalf("expected 1 close position, got %d", len(closePositions))
	}
	if closePositions[0].HoldID != "exec-2" || closePositions[0].Qty != 50 {
		t.Errorf("unexpected close position when skipping exec-1: %+v", closePositions[0])
	}
}

func TestSniperNest_UpdateAndObserve(t *testing.T) {
	detail := symbol.Symbol{Code: "9434"}
	nest := NewSniperNest("9434", detail, nil, nil)
	sniperID := "test-sniper"

	// 1. 注文の作成
	ord := order.NewOrder("order-1", "9434", order.ACTION_BUY, 2000, 100)
	nest.AddOrder(sniperID, ord)

	// 2. 約定レポートの反映
	o1 := order.NewOrder("order-1", "9434", order.ACTION_BUY, 2000, 100)
	o1.BypassTransition(order.ORDER_STATUS_FILLED, order.STATE_CLOSED)
	o1.CumQty = 100
	o1.AddExecution(order.Execution{ID: "exec-1", Price: 2000, Qty: 100, ExecutionTime: time.Now()})

	report := order.Orders{
		Orders: []order.Order{*o1},
	}
	nest.Update(report, time.Now())

	// 3. Observation の確認
	obs := nest.PrepareObservation(sniperID, tick.Tick{Price: 2005}, &strategy.NoopPolicy{})
	if len(obs.Positions) != 1 {
		t.Fatalf("expected 1 position, got %d", len(obs.Positions))
	}
	if obs.HoldQty() != 100 {
		t.Errorf("expected hold qty 100, got %f", obs.HoldQty())
	}
}

func TestSniperNest_Tick_Timeout(t *testing.T) {
	detail := symbol.Symbol{Code: "9434"}
	nest := NewSniperNest("9434", detail, nil, nil)
	policy := &strategy.TouchTTLPolicy{TTL: 2 * time.Second}
	sniperID := "test-sniper"

	ord := order.NewOrder("test-order", "9434", order.ACTION_BUY, 2000, 100)
	ord.BypassTransition(order.ORDER_STATUS_FILL_EXPECTED, order.STATE_ACTIVE)
	ord.Synthetic = order.SyntheticFillState{
		ExpectedAt: time.Now().Add(-30 * time.Second), // タイムアウト済み
	}
	nest.AddOrder(sniperID, ord)

	nest.PrepareObservation(sniperID, tick.Tick{Price: 2000, CurrentPriceTime: time.Now()}, policy)

	if ord.Status() != order.ORDER_STATUS_WAITING {
		t.Errorf("expected status to revert to WAITING, got %v", ord.Status())
	}
}

func TestSniperNest_FailSendingOrder(t *testing.T) {
	detail := symbol.Symbol{Code: "9434"}
	nest := NewSniperNest("9434", detail, nil, nil)
	sniperID := "test-sniper"

	entry := &order.Order{ID: "entry"}
	exit := &order.Order{ID: "exit"}
	nest.AddOrder(sniperID, entry)
	nest.AddOrder(sniperID, exit)

	// 1. 注文の失敗テスト
	nest.FailSendingOrder(sniperID, exit)
	active := nest.GetSniperActiveOrders(sniperID)
	if len(active) != 1 {
		t.Errorf("expected 1 order left, but got %d", len(active))
	}

	nest.FailSendingOrder(sniperID, entry)
	active = nest.GetSniperActiveOrders(sniperID)
	if len(active) != 0 {
		t.Errorf("expected 0 orders left, but got %d", len(active))
	}
}

func TestSniperNest_NoSyntheticFillOnCancelSent(t *testing.T) {
	detail := symbol.Symbol{Code: "9434"}
	nest := NewSniperNest("9434", detail, nil, nil)
	policy := &strategy.StrictPiercePolicy{}
	sniperID := "test-sniper"

	// 1. すでにキャンセル送信済みの注文を用意
	ord := order.NewOrder("test-order", "9434", order.ACTION_BUY, 2000, 100)
	ord.BypassTransition(order.ORDER_STATUS_CANCEL_SENT, order.STATE_CANCELING)
	nest.AddOrder(sniperID, ord)

	// 2. 貫通する Tick を渡す（本来なら FILL_EXPECTED に上書きされる条件）
	nest.PrepareObservation(sniperID, tick.Tick{Price: 1990, CurrentPriceTime: time.Now()}, policy)

	// 3. ステータスが上書きされていないことを確認
	if ord.Status() != order.ORDER_STATUS_CANCEL_SENT {
		t.Errorf("expected status to remain CANCEL_SENT, but got %v", ord.Status())
	}
}
