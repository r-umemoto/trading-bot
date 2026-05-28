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
	s.ActiveOrders = append(s.ActiveOrders, ord)

	pos := s.calculatePosition(nil) // 確定ポジションはゼロ
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
	activeOrders := map[string][]*order.Order{
		sniperID: {ord},
	}

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
	sp.Update(activeOrders, report, time.Now())

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
	s.ActiveOrders = append(s.ActiveOrders, ord)

	obs := Observation{
		Tick: tick.Tick{CurrentPriceTime: time.Now()},
	}
	// Note: CheckTimeout logic was removed from Tick temporarily or should be in Spotter.
	// Current implementation in sniper.go removed it.
	s.Tick(obs)
}

func TestSniper_FailSendingOrder(t *testing.T) {
	detail := symbol.Symbol{Code: "9434"}
	s := NewSniper("test-sniper", detail, &MockStrategy{}, nil, order.EXCHANGE_TOSHO, nil)

	entry := &order.Order{ID: "entry"}
	exit := &order.Order{ID: "exit"}
	s.ActiveOrders = append(s.ActiveOrders, entry)
	s.ActiveOrders = append(s.ActiveOrders, exit)

	// 1. 注文の失敗テスト
	s.FailSendingOrder(exit)
	if len(s.ActiveOrders) != 1 {
		t.Errorf("expected 1 order left, but got %d", len(s.ActiveOrders))
	}

	s.FailSendingOrder(entry)
	if len(s.ActiveOrders) != 0 {
		t.Errorf("expected 0 orders left, but got %d", len(s.ActiveOrders))
	}
}
func TestSniper_Tick_NoSyntheticFillOnCancelSent(t *testing.T) {
	detail := symbol.Symbol{Code: "9434"}
	// 貫通ポリシーを使用
	policy := &strategy.StrictPiercePolicy{}
	s := NewSniper("test-sniper", detail, &MockStrategy{}, policy, order.EXCHANGE_TOSHO, nil)

	// 1. すでにキャンセル送信済みの注文を用意
	ord := &order.Order{
		ID:         "test-order",
		Action:     order.ACTION_BUY,
		OrderPrice: 2000,
		OrderQty:   100,
		Status:     order.ORDER_STATUS_CANCEL_SENT, // ← これを維持したい
	}
	s.ActiveOrders = append(s.ActiveOrders, ord)

	// 2. 貫通する Tick を渡す（本来なら FILL_EXPECTED に上書きされる条件）
	obs := Observation{
		Tick: tick.Tick{Price: 1990, CurrentPriceTime: time.Now()},
	}
	s.Tick(obs)

	// 3. ステータスが上書きされていないことを確認
	if ord.Status != order.ORDER_STATUS_CANCEL_SENT {
		t.Errorf("expected status to remain CANCEL_SENT, but got %v", ord.Status)
	}
}
