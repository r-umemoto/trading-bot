package sniper

import (
	"log/slog"
	"testing"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/brain"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
)

type MockStrategy struct{}

func (m *MockStrategy) Name() string                                       { return "mock" }
func (m *MockStrategy) Evaluate(input strategy.StrategyInput) brain.Signal { return brain.Signal{} }
func (m *MockStrategy) AnalysisLogger() *slog.Logger                       { return nil }
func (m *MockStrategy) IfDone(input strategy.StrategyInput, prevSignal brain.Signal) brain.Signal {
	return brain.Signal{Action: brain.ACTION_HOLD}
}

func TestSniper_SyncOrders(t *testing.T) {
	detail := symbol.Symbol{Code: "9434"}
	policy := &strategy.NoopPolicy{}
	s := NewSniper(detail, &MockStrategy{}, policy, order.EXCHANGE_TOSHO, nil, nil)

	// 1. 注文を発注した直後の状態（PENDING ID）
	pendingID := order.GenerateLocalID()
	internalOrder := order.NewOrder(pendingID, "9434", order.ACTION_BUY, 2000, 100)
	s.Orders = append(s.Orders, internalOrder)

	// 2. 取引所から正式なIDが割り当てられたことを反映する (Gatewayが行う処理のモック)
	realID := "order-123"
	internalOrder.ID = realID

	// 内部の注文IDが更新されているか確認
	if internalOrder.ID != realID {
		t.Fatalf("expected internal ID to be updated to %s, got %s", realID, internalOrder.ID)
	}

	// 3. 同期処理：取引所から報告が届く
	extOrders := []order.Order{
		{
			ID:         realID,
			Symbol:     "9434",
			Action:     order.ACTION_BUY,
			OrderPrice: 2000,
			OrderQty:   100,
			Status:     order.ORDER_STATUS_IN_PROGRESS,
		},
	}
	s.SyncOrders(order.Orders{Orders: extOrders})

	// 4. キャンセル送信中の状態にする
	internalOrder.Status = order.ORDER_STATUS_CANCEL_SENT

	// 5. 取引所からキャンセル完了（CANCELED）の報告が届く
	extOrders = []order.Order{
		{
			ID:     realID,
			Symbol: "9434",
			Status: order.ORDER_STATUS_CANCELED,
		},
	}
	s.SyncOrders(order.Orders{Orders: extOrders})

	// ステータスが CANCELED に更新されていることを確認
	if internalOrder.Status != order.ORDER_STATUS_CANCELED {
		t.Errorf("expected status CANCELED(4), got %d", internalOrder.Status)
	}

	if !internalOrder.IsCompleted() {
		t.Errorf("order should be completed after cancellation")
	}
}

func TestSniper_SyncOrders_Executions(t *testing.T) {
	detail := symbol.Symbol{Code: "9434"}
	policy := &strategy.NoopPolicy{}
	s := NewSniper(detail, &MockStrategy{}, policy, order.EXCHANGE_TOSHO, nil, nil)
	realID := "order-456"
	internalOrder := order.NewOrder(realID, "9434", order.ACTION_BUY, 2000, 100)
	s.Orders = append(s.Orders, internalOrder)

	// 約定が発生した報告が届く
	extOrders := []order.Order{
		{
			ID:     realID,
			Symbol: "9434",
			Status: order.ORDER_STATUS_IN_PROGRESS,
			CumQty: 50,
			Executions: []order.Execution{
				{ID: "exec-1", Price: 2000, Qty: 50},
			},
		},
	}

	s.SyncOrders(order.Orders{Orders: extOrders})

	// ポジションが更新されているか確認
	holdQty := 0.0
	for _, p := range s.positions {
		holdQty += p.LeavesQty
	}
	if holdQty != 50 {
		t.Errorf("expected holdQty 50, got %f", holdQty)
	}

	// 同じ報告が再度届いても、二重計上されないか確認（冪等性）
	s.SyncOrders(order.Orders{Orders: extOrders})
	holdQty = 0.0
	for _, p := range s.positions {
		holdQty += p.LeavesQty
	}
	if holdQty != 50 {
		t.Errorf("expected holdQty 50 (after deduplication), got %f", holdQty)
	}
}
