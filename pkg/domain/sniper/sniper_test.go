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
func (m *MockStrategy) ShouldCancel(input strategy.StrategyInput, ord *order.Order) bool {
	return false
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

	if bullet != nil {
		t.Error("expected no bullet for empty signal")
	}

	// 2. 疑似約定(FILL_EXPECTED)がある場合のポジション計算テスト
	ord := order.NewOrder("test-order", "7203", order.ACTION_BUY, 2000, 100)
	ord.BypassTransition(order.ORDER_STATUS_FILL_EXPECTED, ord.InternalState())

	obs2 := Observation{
		Tick:         tick.Tick{Price: 2000, CurrentPriceTime: time.Now()},
		ActiveOrders: []*order.Order{ord},
	}

	pos := s.calculatePosition(obs2) // 確定ポジションはゼロ
	if pos.Qty != 100 {
		t.Errorf("expected position 100 due to synthetic fill, got %f", pos.Qty)
	}
}

func TestSpotter_UpdateAndObserve(t *testing.T) {
	detail := symbol.Symbol{Code: "9434"}
	sp := NewSpotter(detail, nil)
	sniperID := "test-sniper"

	// 1. 注文の作成
	ord := order.NewOrder("order-1", "9434", order.ACTION_BUY, 2000, 100)
	sp.AddOrder(sniperID, ord)

	// 2. 約定レポートの反映
	o1 := order.NewOrder("order-1", "9434", order.ACTION_BUY, 2000, 100)
	o1.BypassTransition(order.ORDER_STATUS_FILLED, order.STATE_CLOSED)
	o1.CumQty = 100
	o1.AddExecution(order.Execution{ID: "exec-1", Price: 2000, Qty: 100, ExecutionTime: time.Now()})

	report := order.Orders{
		Orders: []order.Order{*o1},
	}
	sp.Update(report, time.Now())

	// 3. Observation の確認
	obs := sp.PrepareObservation(sniperID, tick.Tick{Price: 2005}, &strategy.NoopPolicy{})
	if len(obs.Positions) != 1 {
		t.Fatalf("expected 1 position, got %d", len(obs.Positions))
	}
	if obs.HoldQty() != 100 {
		t.Errorf("expected hold qty 100, got %f", obs.HoldQty())
	}
}

func TestSpotter_Tick_Timeout(t *testing.T) {
	detail := symbol.Symbol{Code: "9434"}
	sp := NewSpotter(detail, nil)
	policy := &strategy.TouchTTLPolicy{TTL: 2 * time.Second}
	sniperID := "test-sniper"

	ord := order.NewOrder("test-order", "9434", order.ACTION_BUY, 2000, 100)
	ord.BypassTransition(order.ORDER_STATUS_FILL_EXPECTED, order.STATE_ACTIVE)
	ord.Synthetic = order.SyntheticFillState{
		ExpectedAt: time.Now().Add(-30 * time.Second), // タイムアウト済み
	}
	sp.AddOrder(sniperID, ord)

	sp.PrepareObservation(sniperID, tick.Tick{Price: 2000, CurrentPriceTime: time.Now()}, policy)

	if ord.Status() != order.ORDER_STATUS_WAITING {
		t.Errorf("expected status to revert to WAITING, got %v", ord.Status())
	}
}

func TestSpotter_FailSendingOrder(t *testing.T) {
	detail := symbol.Symbol{Code: "9434"}
	sp := NewSpotter(detail, nil)
	sniperID := "test-sniper"

	entry := &order.Order{ID: "entry"}
	exit := &order.Order{ID: "exit"}
	sp.AddOrder(sniperID, entry)
	sp.AddOrder(sniperID, exit)

	// 1. 注文の失敗テスト
	sp.FailSendingOrder(sniperID, exit)
	active := sp.GetSniperActiveOrders(sniperID)
	if len(active) != 1 {
		t.Errorf("expected 1 order left, but got %d", len(active))
	}

	sp.FailSendingOrder(sniperID, entry)
	active = sp.GetSniperActiveOrders(sniperID)
	if len(active) != 0 {
		t.Errorf("expected 0 orders left, but got %d", len(active))
	}
}

func TestSpotter_NoSyntheticFillOnCancelSent(t *testing.T) {
	detail := symbol.Symbol{Code: "9434"}
	sp := NewSpotter(detail, nil)
	policy := &strategy.StrictPiercePolicy{}
	sniperID := "test-sniper"

	// 1. すでにキャンセル送信済みの注文を用意
	ord := order.NewOrder("test-order", "9434", order.ACTION_BUY, 2000, 100)
	ord.BypassTransition(order.ORDER_STATUS_CANCEL_SENT, order.STATE_CANCELING)
	sp.AddOrder(sniperID, ord)

	// 2. 貫通する Tick を渡す（本来なら FILL_EXPECTED に上書きされる条件）
	sp.PrepareObservation(sniperID, tick.Tick{Price: 1990, CurrentPriceTime: time.Now()}, policy)

	// 3. ステータスが上書きされていないことを確認
	if ord.Status() != order.ORDER_STATUS_CANCEL_SENT {
		t.Errorf("expected status to remain CANCEL_SENT, but got %v", ord.Status())
	}
}
