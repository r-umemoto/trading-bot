package sniper

import (
	"log/slog"
	"testing"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/brain"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

type MockStrategy struct{}

func (m *MockStrategy) Name() string                                       { return "mock" }
func (m *MockStrategy) Evaluate(input strategy.StrategyInput) brain.Signal { return brain.Signal{} }
func (m *MockStrategy) AnalysisLogger() *slog.Logger                       { return nil }
func (m *MockStrategy) IfDone(input strategy.StrategyInput, prevSignal brain.Signal) brain.Signal {
	return brain.Signal{Action: brain.ACTION_HOLD}
}

func TestSniper_Tick_WithObservation(t *testing.T) {
	detail := symbol.Symbol{Code: "9434"}
	policy := &strategy.NoopPolicy{}
	s := NewSniper("test-sniper", detail, &MockStrategy{}, policy, order.EXCHANGE_TOSHO, nil)

	// 1. 空の Observation で Tick を実行
	obs := Observation{
		Tick: tick.Tick{Price: 2000, CurrentPriceTime: time.Now()},
	}
	bullet := s.Tick(obs)

	if bullet.HasOrder() {
		t.Error("expected no order for empty signal")
	}

	// 2. 疑似約定(FILL_EXPECTED)がある場合のポジション計算テスト
	ord := &order.Order{
		ID:         "test-order",
		Action:     order.ACTION_BUY,
		OrderPrice: 2000,
		OrderQty:   100,
		Status:     order.ORDER_STATUS_FILL_EXPECTED,
	}
	s.ManagedOrders = append(s.ManagedOrders, NewManagedOrder("test-order", ord, nil))

	pos := s.calculatePosition(nil) // 確定ポジションはゼロ
	if pos.Qty != 100 {
		t.Errorf("expected position 100 due to synthetic fill, got %f", pos.Qty)
	}
}

func TestSpotter_UpdateAndObserve(t *testing.T) {
	detail := symbol.Symbol{Code: "9434"}
	sp := NewSpotter(detail, nil)
	sniperID := "test-sniper"

	// 1. 注文の追加
	ord := order.NewOrder("order-1", "9434", order.ACTION_BUY, 2000, 100)
	sp.RecordBullet(sniperID, Bullet{Order: ord})

	// 2. 約定レポートの反映
	report := order.Orders{
		Orders: []order.Order{
			{
				ID:     "order-1",
				Symbol: "9434",
				Status: order.ORDER_STATUS_FILLED,
				CumQty: 100,
				Executions: []order.Execution{
					{ID: "exec-1", Price: 2000, Qty: 100, ExecutionTime: time.Now()},
				},
			},
		},
	}
	sp.Update(report, time.Now())

	// 3. Observation の確認
	obs := sp.PrepareObservation(sniperID, tick.Tick{Price: 2005})
	if len(obs.Positions) != 1 {
		t.Fatalf("expected 1 position, got %d", len(obs.Positions))
	}
	if obs.HoldQty() != 100 {
		t.Errorf("expected hold qty 100, got %f", obs.HoldQty())
	}
}

func TestSniper_Tick_Timeout(t *testing.T) {
	detail := symbol.Symbol{Code: "9434"}
	policy := &strategy.NoopPolicy{}
	s := NewSniper("test-sniper", detail, &MockStrategy{}, policy, order.EXCHANGE_TOSHO, nil)

	ord := &order.Order{
		Status: order.ORDER_STATUS_FILL_EXPECTED,
		Synthetic: order.SyntheticFillState{
			ExpectedAt: time.Now().Add(-30 * time.Second), // タイムアウト済み
		},
	}
	s.ManagedOrders = append(s.ManagedOrders, NewManagedOrder("test-order", ord, nil))

	obs := Observation{
		Tick: tick.Tick{CurrentPriceTime: time.Now()},
	}
	s.Tick(obs) // タイムアウト判定は Tick で行われる

	if ord.Status != order.ORDER_STATUS_IN_PROGRESS {
		t.Errorf("expected status to revert to IN_PROGRESS due to timeout, got %v", ord.Status)
	}
}

func TestSniper_FailSendingOrder(t *testing.T) {
	detail := symbol.Symbol{Code: "9434"}
	s := NewSniper("test-sniper", detail, &MockStrategy{}, nil, order.EXCHANGE_TOSHO, nil)

	entry := &order.Order{ID: "entry"}
	exit := &order.Order{ID: "exit"}
	mo := NewManagedOrder("mo-1", entry, exit)
	s.ManagedOrders = append(s.ManagedOrders, mo)

	// 1. 決済注文(Exit)の失敗テスト
	s.FailSendingOrder(exit)
	if len(s.ManagedOrders) != 1 {
		t.Errorf("expected ManagedOrder to be retained after exit failure, but got length %d", len(s.ManagedOrders))
	}
	if s.ManagedOrders[0].Status != StatusEntryActive {
		t.Errorf("expected ManagedOrder status to be StatusEntryActive after exit failure, but got %v", s.ManagedOrders[0].Status)
	}

	// 2. エントリー注文(Entry)の失敗テスト
	s.FailSendingOrder(entry)
	if len(s.ManagedOrders) != 0 {
		t.Errorf("expected ManagedOrder to be removed after entry failure, but got length %d", len(s.ManagedOrders))
	}
}
