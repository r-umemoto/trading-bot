package sniper

import (
	"log/slog"
	"testing"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/position"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/brain"
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

	nestA := NewSniperNest("7203", NewSpotter(detailA, nil), []*Sniper{sniperA})
	nestB := NewSniperNest("7267", NewSpotter(detailB, nil), []*Sniper{sniperB})

	dataPool := tick.NewDefaultDataPool(&DummyHistoricalFeederProvider{})

	// threshold = 10.0, qty = 100
	o := NewPairTradingOperation("test-pair", nestA, nestB, stratA, stratB, dataPool, 10.0, 100.0, slog.Default())

	// 2. エントリー可能な黄金時間帯でのスプレッド乖離 (10:00 JST, Spread = 15.0)
	timeAllowed, _ := time.ParseInLocation(time.RFC3339, "2026-06-01T10:00:00+09:00", loc)
	tickA := tick.Tick{Symbol: "7203", Price: 1015.0, CurrentPriceTime: timeAllowed}
	tickB := tick.Tick{Symbol: "7267", Price: 1000.0, CurrentPriceTime: timeAllowed}
	dataPool.PushTick(tickA)
	dataPool.PushTick(tickB)

	// シグナルを一旦リセット
	stratA.SetSignal(brain.Signal{Action: brain.ACTION_HOLD})
	stratB.SetSignal(brain.Signal{Action: brain.ACTION_HOLD})

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
	timeForbidden, _ := time.ParseInLocation(time.RFC3339, "2026-06-01T09:15:00+09:00", loc)
	tickA.CurrentPriceTime = timeForbidden
	tickB.CurrentPriceTime = timeForbidden
	dataPool.PushTick(tickA)
	dataPool.PushTick(tickB)

	stratA.SetSignal(brain.Signal{Action: brain.ACTION_HOLD})
	stratB.SetSignal(brain.Signal{Action: brain.ACTION_HOLD})

	actions = o.HandleTick(tickA)

	if len(actions) != 0 {
		t.Errorf("Expected 0 entry actions in forbidden window, got %d", len(actions))
	}

	// 4. すでにポジションがある場合の決済判定 (14:50 JST - エントリー禁止時間だが決済は許可されるべき)
	// 前のテストケースで作成された未完了の発注履歴(ActiveOrders)をクリアしてブロックを解除
	sniperA.ActiveOrders = nil
	sniperB.ActiveOrders = nil

	// ポジションをセット (Hold Qty = 100)
	nestA.spotter.sniperPositions["sniper-a"] = []position.Position{
		{ExecutionID: "exec-a", Symbol: "7203", LeavesQty: 100, Price: 1000.0, Action: order.ACTION_BUY},
	}
	nestB.spotter.sniperPositions["sniper-b"] = []position.Position{
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

	stratA.SetSignal(brain.Signal{Action: brain.ACTION_HOLD})
	stratB.SetSignal(brain.Signal{Action: brain.ACTION_HOLD})

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
