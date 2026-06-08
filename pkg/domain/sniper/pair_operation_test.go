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

// DummyHistoricalFeederProvider はテスト用のダミープロバイダーです
type DummyHistoricalFeederProvider struct{}

func (p *DummyHistoricalFeederProvider) GetFeeder(symbol string) tick.HistoricalFeeder {
	return nil
}

func TestPairTradingOperation_IsAllowedTimeForEntry(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Skip("Asia/Tokyo location not found")
	}

	o := &PairTradingOperation{}

	tests := []struct {
		name     string
		timeStr  string
		expected bool
	}{
		{"寄付直後 09:15 JST (禁止)", "2026-06-01T09:15:00+09:00", false},
		{"前場安定期 09:30 JST (許可)", "2026-06-01T09:30:00+09:00", true},
		{"前場安定期 10:30 JST (許可)", "2026-06-01T10:30:00+09:00", true},
		{"前場終了直前 11:29 JST (許可)", "2026-06-01T11:29:00+09:00", true},
		{"昼休み 11:45 JST (禁止)", "2026-06-01T11:45:00+09:00", false},
		{"後場寄付直後 12:35 JST (禁止)", "2026-06-01T12:35:00+09:00", false},
		{"後場安定期 13:00 JST (許可)", "2026-06-01T13:00:00+09:00", true},
		{"後場安定期 14:15 JST (許可)", "2026-06-01T14:15:00+09:00", true},
		{"大引け前 14:45 JST (禁止)", "2026-06-01T14:45:00+09:00", false},
		{"大引け前 14:55 JST (禁止)", "2026-06-01T14:55:00+09:00", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parseTime, err := time.ParseInLocation(time.RFC3339, tt.timeStr, loc)
			if err != nil {
				t.Fatalf("failed to parse time: %v", err)
			}
			result := o.isAllowedTimeForEntry(parseTime)
			if result != tt.expected {
				t.Errorf("expected %v, got %v for time %s", tt.expected, result, tt.timeStr)
			}
		})
	}
}

func TestPairTradingOperation_HandleTick_TimeFilter(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Skip("Asia/Tokyo location not found")
	}

	// 1. 各モックの設定
	detailA := symbol.Symbol{Code: "7203"}
	detailB := symbol.Symbol{Code: "7267"}

	// Sniper A & B
	stratA := NewInstructionStrategy()
	stratB := NewInstructionStrategy()
	sniperA := NewSniper("sniper-a", detailA, stratA, &strategy.NoopPolicy{}, order.EXCHANGE_TOSHO, nil)
	sniperB := NewSniper("sniper-b", detailB, stratB, &strategy.NoopPolicy{}, order.EXCHANGE_TOSHO, nil)

	nestA := NewSniperNest("7203", detailA, []*Sniper{sniperA}, nil)
	nestB := NewSniperNest("7267", detailB, []*Sniper{sniperB}, nil)

	dataPool := tick.NewDefaultDataPool(&DummyHistoricalFeederProvider{})

	// threshold = 0.01 (1%), qty = 100
	o := NewPairTradingOperation("test-pair", nestA, nestB, stratA, stratB, dataPool, 0.01, 100.0, slog.Default())

	// 2. エントリー可能な黄金時間帯でのスプレッド乖離 (10:00 JST, Spread = 1.5% > 1.0% threshold)
	timeAllowed, _ := time.ParseInLocation(time.RFC3339, "2026-06-01T10:00:00+09:00", loc)
	tickA := tick.Tick{Symbol: "7203", Price: 1015.0, CurrentPriceTime: timeAllowed, OpeningPrice: 1000.0}
	tickB := tick.Tick{Symbol: "7267", Price: 1000.0, CurrentPriceTime: timeAllowed, OpeningPrice: 1000.0}
	dataPool.PushTick(tickA)
	dataPool.PushTick(tickB)

	// ターゲットを一旦リセット
	stratA.SetTarget(strategy.TargetPosition{Qty: 0})
	stratB.SetTarget(strategy.TargetPosition{Qty: 0})

	// 判定実行
	actions := o.HandleTick(tickA)

	if len(actions) != 2 {
		t.Errorf("Expected 2 entry actions inside allowed golden window, got %d", len(actions))
	} else {
		actionMap := make(map[string]order.Action)
		for _, act := range actions {
			if b, ok := act.Bullet.(OrderBullet); ok {
				actionMap[act.SniperID] = b.Order.Action
			}
		}
		if actionMap["sniper-a"] != order.ACTION_SELL || actionMap["sniper-b"] != order.ACTION_BUY {
			t.Errorf("Expected A to be SELL and B to be BUY, got A: %v, B: %v", actionMap["sniper-a"], actionMap["sniper-b"])
		}
	}

	// 3. エントリー不可の時間帯でのスプレッド乖離 (09:15 JST, Spread = 15.0)
	// 前のテストケースで作成された未完了の発注履歴(ActiveOrders)をクリアしてブロックを解除
	nestA.orders.activeOrders["sniper-a"] = nil
	nestB.orders.activeOrders["sniper-b"] = nil

	timeForbidden, _ := time.ParseInLocation(time.RFC3339, "2026-06-01T09:15:00+09:00", loc)
	tickA.CurrentPriceTime = timeForbidden
	tickB.CurrentPriceTime = timeForbidden
	dataPool.PushTick(tickA)
	dataPool.PushTick(tickB)

	stratA.SetTarget(strategy.TargetPosition{Qty: 0})
	stratB.SetTarget(strategy.TargetPosition{Qty: 0})

	actions = o.HandleTick(tickA)

	if len(actions) != 0 {
		t.Errorf("Expected 0 entry actions in forbidden window, got %d", len(actions))
	}

	// 4. すでにポジションがある場合の決済判定 (14:50 JST - エントリー禁止時間だが決済は許可されるべき)
	// 前のテストケースで作成された未完了の発注履歴(ActiveOrders)をクリアしてブロックを解除
	nestA.orders.activeOrders["sniper-a"] = nil
	nestB.orders.activeOrders["sniper-b"] = nil

	// ポジションをセット (Hold Qty = 100)
	nestA.positions.positions["sniper-a"] = []position.Position{
		{ExecutionID: "exec-a", Symbol: "7203", LeavesQty: 100, Price: 1000.0, Action: order.ACTION_BUY},
	}
	nestB.positions.positions["sniper-b"] = []position.Position{
		{ExecutionID: "exec-b", Symbol: "7267", LeavesQty: 100, Price: 1000.0, Action: order.ACTION_SELL},
	}

	// スプレッドが平均に収束 (Spread = 0.5 < 1.0)
	timeExitForbidden, _ := time.ParseInLocation(time.RFC3339, "2026-06-01T14:50:00+09:00", loc)
	tickA.Price = 1000.5
	tickB.Price = 1000.0
	tickA.CurrentPriceTime = timeExitForbidden
	tickB.CurrentPriceTime = timeExitForbidden
	dataPool.PushTick(tickA)
	dataPool.PushTick(tickB)

	stratA.SetTarget(strategy.TargetPosition{Qty: 0})
	stratB.SetTarget(strategy.TargetPosition{Qty: 0})

	actions = o.HandleTick(tickA)

	if len(actions) != 2 {
		t.Errorf("Expected 2 exit actions even in forbidden window, got %d", len(actions))
	} else {
		actionMap := make(map[string]order.Action)
		for _, act := range actions {
			if b, ok := act.Bullet.(OrderBullet); ok {
				actionMap[act.SniperID] = b.Order.Action
			}
		}
		if actionMap["sniper-a"] != order.ACTION_SELL || actionMap["sniper-b"] != order.ACTION_BUY {
			t.Errorf("Expected exit A to be SELL and B to be BUY, got A: %v, B: %v", actionMap["sniper-a"], actionMap["sniper-b"])
		}
	}
}

func TestPairTradingOperation_HandleTick_NegativeSpreadAndShortExit(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Tokyo")

	detailA := symbol.Symbol{Code: "7203"}
	detailB := symbol.Symbol{Code: "7267"}

	stratA := NewInstructionStrategy()
	stratB := NewInstructionStrategy()
	sniperA := NewSniper("sniper-a", detailA, stratA, &strategy.NoopPolicy{}, order.EXCHANGE_TOSHO, nil)
	sniperB := NewSniper("sniper-b", detailB, stratB, &strategy.NoopPolicy{}, order.EXCHANGE_TOSHO, nil)

	nestA := NewSniperNest("7203", detailA, []*Sniper{sniperA}, nil)
	nestB := NewSniperNest("7267", detailB, []*Sniper{sniperB}, nil)

	dataPool := tick.NewDefaultDataPool(&DummyHistoricalFeederProvider{})
	o := NewPairTradingOperation("test-pair", nestA, nestB, stratA, stratB, dataPool, 0.01, 100.0, nil)

	// 1. Negative Spread Entry (Spread = -1.5% < -1.0% threshold) -> A Buy, B Sell
	timeAllowed, _ := time.ParseInLocation(time.RFC3339, "2026-06-01T10:00:00+09:00", loc)
	tickA := tick.Tick{Symbol: "7203", Price: 1000.0, CurrentPriceTime: timeAllowed, OpeningPrice: 1000.0}
	tickB := tick.Tick{Symbol: "7267", Price: 1015.0, CurrentPriceTime: timeAllowed, OpeningPrice: 1000.0}
	dataPool.PushTick(tickA)
	dataPool.PushTick(tickB)

	actions := o.HandleTick(tickA)
	if len(actions) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(actions))
	}
	actionMap := make(map[string]order.Action)
	for _, act := range actions {
		if b, ok := act.Bullet.(OrderBullet); ok {
			actionMap[act.SniperID] = b.Order.Action
		}
	}
	if actionMap["sniper-a"] != order.ACTION_BUY || actionMap["sniper-b"] != order.ACTION_SELL {
		t.Errorf("expected A to BUY and B to SELL, got A: %v, B: %v", actionMap["sniper-a"], actionMap["sniper-b"])
	}

	// 2. Exit when Short A / Long B
	nestA.orders.activeOrders["sniper-a"] = nil
	nestB.orders.activeOrders["sniper-b"] = nil

	// Set Short A (-100 Qty) and Long B (100 Qty) positions
	nestA.positions.positions["sniper-a"] = []position.Position{
		{ExecutionID: "exec-a", Symbol: "7203", LeavesQty: 100, Price: 1000.0, Action: order.ACTION_SELL},
	}
	nestB.positions.positions["sniper-b"] = []position.Position{
		{ExecutionID: "exec-b", Symbol: "7267", LeavesQty: 100, Price: 1000.0, Action: order.ACTION_BUY},
	}

	// Spread reverts to mean
	tickA.Price = 1000.0
	tickB.Price = 1000.1
	dataPool.PushTick(tickA)
	dataPool.PushTick(tickB)

	actions = o.HandleTick(tickA)
	if len(actions) != 2 {
		t.Fatalf("expected 2 exit actions, got %d", len(actions))
	}
	actionMapExit := make(map[string]order.Action)
	for _, act := range actions {
		if b, ok := act.Bullet.(OrderBullet); ok {
			actionMapExit[act.SniperID] = b.Order.Action
		}
	}
	// Exit A should be BUY (to close Short), Exit B should be SELL (to close Long)
	if actionMapExit["sniper-a"] != order.ACTION_BUY || actionMapExit["sniper-b"] != order.ACTION_SELL {
		t.Errorf("expected exit A to be BUY and B to be SELL, got A: %v, B: %v", actionMapExit["sniper-a"], actionMapExit["sniper-b"])
	}
}

func TestPairTradingOperation_InterfaceMethods(t *testing.T) {
	detailA := symbol.Symbol{Code: "7203"}
	detailB := symbol.Symbol{Code: "7267"}

	stratA := NewInstructionStrategy()
	stratB := NewInstructionStrategy()
	sniperA := NewSniper("sniper-a", detailA, stratA, &strategy.NoopPolicy{}, order.EXCHANGE_TOSHO, nil)
	sniperB := NewSniper("sniper-b", detailB, stratB, &strategy.NoopPolicy{}, order.EXCHANGE_SOR, nil)

	nestA := NewSniperNest("7203", detailA, []*Sniper{sniperA}, nil)
	nestB := NewSniperNest("7267", detailB, []*Sniper{sniperB}, nil)

	dataPool := tick.NewDefaultDataPool(&DummyHistoricalFeederProvider{})
	o := NewPairTradingOperation("test-pair", nestA, nestB, stratA, stratB, dataPool, 0.01, 100.0, nil)

	// GetID
	if o.GetID() != "test-pair" {
		t.Errorf("expected test-pair, got %s", o.GetID())
	}

	// GetSymbolCode
	if o.GetSymbolCode() != "7203" {
		t.Errorf("expected 7203, got %s", o.GetSymbolCode())
	}

	// GetSymbolCodes
	codes := o.GetSymbolCodes()
	if len(codes) != 2 || codes[0] != "7203" || codes[1] != "7267" {
		t.Errorf("unexpected symbol codes: %v", codes)
	}

	// GetExchanges
	exchanges := o.GetExchanges()
	if len(exchanges) != 2 {
		t.Fatalf("expected 2 exchanges, got %d", len(exchanges))
	}

	// HasSniper
	if !o.HasSniper("sniper-a") || !o.HasSniper("sniper-b") || o.HasSniper("unknown") {
		t.Error("HasSniper returned invalid results")
	}

	// GetReportableTargets
	targets := o.GetReportableTargets()
	if len(targets) != 2 {
		t.Errorf("expected 2 targets, got %d", len(targets))
	}

	// ActiveOrders and UpdateOrders / FailSendingOrder / UpdateOrderID / GetPerformance / GetUnrealizedPnL
	ord := order.NewOrder("order-temp", "7203", order.ACTION_BUY, 2000, 100)
	nestA.AddOrder("sniper-a", ord)

	if len(o.GetActiveOrders()) != 1 {
		t.Error("expected 1 active order")
	}

	o.UpdateOrders(order.Orders{
		Orders: []order.Order{*ord},
	})

	o.UpdateOrderID("sniper-a", ord, "new-api-id")
	if o.GetActiveOrders()[0].ID != "new-api-id" {
		t.Error("expected ID to be updated")
	}

	// Test update on nestB just to cover branch
	ordB := order.NewOrder("order-temp-b", "7267", order.ACTION_BUY, 2000, 100)
	nestB.AddOrder("sniper-b", ordB)
	o.UpdateOrderID("sniper-b", ordB, "new-api-id-b")
	o.FailSendingOrder("sniper-b", ordB)

	o.FailSendingOrder("sniper-a", ord)
	if len(o.GetActiveOrders()) != 0 {
		t.Error("expected active orders to be empty after fail sending")
	}

	// GetPerformance & GetUnrealizedPnL
	perfA := o.GetPerformance("sniper-a")
	if perfA.Trades != 0 {
		t.Error("expected empty performance")
	}
	perfB := o.GetPerformance("sniper-b")
	if perfB.Trades != 0 {
		t.Error("expected empty performance")
	}
	perfNone := o.GetPerformance("none")
	if perfNone.Trades != 0 {
		t.Error("expected empty performance")
	}

	pnlA := o.GetUnrealizedPnL("sniper-a", 2000.0)
	if pnlA != 0.0 {
		t.Error("expected zero pnl")
	}
	pnlB := o.GetUnrealizedPnL("sniper-b", 2000.0)
	if pnlB != 0.0 {
		t.Error("expected zero pnl")
	}
	pnlNone := o.GetUnrealizedPnL("none", 2000.0)
	if pnlNone != 0.0 {
		t.Error("expected zero pnl")
	}

	// ForceExit
	o.ForceExit()
	if sniperA.GetLifecycle() != LifecycleStopped || sniperB.GetLifecycle() != LifecycleStopped {
		t.Error("expected lifecycle to be stopped after ForceExit")
	}
}

func TestPairTradingStrategyFactory(t *testing.T) {
	factory, err := strategy.GetFactory("pair_trading")
	if err != nil {
		t.Fatalf("failed to get pair_trading factory: %v", err)
	}

	sym := symbol.Symbol{Code: "7203"}
	dataPool := tick.NewDefaultDataPool(&DummyHistoricalFeederProvider{})

	strat := factory.NewStrategy(sym, dataPool, nil)
	if strat.Name() != "InstructionStrategy" {
		t.Errorf("expected InstructionStrategy, got %s", strat.Name())
	}
	if strat.AnalysisLogger() != nil {
		t.Error("expected nil logger")
	}


	policy := factory.CreateExecutionPolicy(nil)
	if policy == nil {
		t.Fatal("expected execution policy not to be nil")
	}
}

func TestPairTradingOperation_HandleTick_ZeroStates(t *testing.T) {
	detailA := symbol.Symbol{Code: "7203"}
	detailB := symbol.Symbol{Code: "7267"}
	stratA := NewInstructionStrategy()
	stratB := NewInstructionStrategy()
	nestA := NewSniperNest("7203", detailA, nil, nil)
	nestB := NewSniperNest("7267", detailB, nil, nil)
	dataPool := tick.NewDefaultDataPool(&DummyHistoricalFeederProvider{})
	o := NewPairTradingOperation("test-pair", nestA, nestB, stratA, stratB, dataPool, 0.01, 100.0, nil)

	// Zero state should return nil
	actions := o.HandleTick(tick.Tick{Symbol: "7203"})
	if actions != nil {
		t.Error("expected nil actions when state is zero")
	}
}
