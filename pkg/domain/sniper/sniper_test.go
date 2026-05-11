package sniper

import (
	"testing"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/brain"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
)

type MockStrategy struct{}

func (m *MockStrategy) Name() string                                { return "mock" }
func (m *MockStrategy) Evaluate(input strategy.StrategyInput) brain.Signal { return brain.Signal{} }

func TestSniper_SyncOrders(t *testing.T) {
	detail := market.Symbol{Code: "9434"}
	policy := &strategy.NoopPolicy{} // 何もしないポリシー
	s := NewSniper(detail, &MockStrategy{}, policy, market.EXCHANGE_TOSHO)

	// 1. 注文を発注した直後の状態（PENDING ID）
	pendingID := market.GeneratePendingID()
	internalOrder := market.NewOrderPtr(pendingID, "9434", market.ACTION_BUY, 2000, 100)
	s.Orders = append(s.Orders, internalOrder)

	// 2. 取引所から正式なIDが割り当てられた報告が届く
	realID := "order-123"
	extOrders := []market.Order{
		{
			ID:         realID,
			Symbol:     "9434",
			Action:     market.ACTION_BUY,
			OrderPrice: 2000,
			OrderQty:   100,
			Status:     market.ORDER_STATUS_IN_PROGRESS,
		},
	}

	// SyncOrders を呼び出し（IDの紐付けが発生するはず）
	_, _, confirmedID := s.SyncOrders(extOrders)
	if confirmedID != realID {
		t.Errorf("expected confirmedID %s, got %s", realID, confirmedID)
	}

	// 内部の注文IDが更新されているか確認
	if internalOrder.ID != realID {
		t.Errorf("expected internal ID to be updated to %s, got %s", realID, internalOrder.ID)
	}

	// 3. キャンセル送信中の状態にする
	internalOrder.Status = market.ORDER_STATUS_CANCEL_SENT

	// 4. 取引所からキャンセル完了（CANCELED）の報告が届く
	extOrders = []market.Order{
		{
			ID:     realID,
			Status: market.ORDER_STATUS_CANCELED,
		},
	}
	s.SyncOrders(extOrders)

	if internalOrder.Status != market.ORDER_STATUS_CANCELED {
		t.Errorf("expected status CANCELED, got %d", internalOrder.Status)
	}

	if !internalOrder.IsCompleted() {
		t.Errorf("order should be completed after cancellation")
	}
}

func TestSniper_SyncOrders_Executions(t *testing.T) {
	detail := market.Symbol{Code: "9434"}
	policy := &strategy.NoopPolicy{}
	s := NewSniper(detail, &MockStrategy{}, policy, market.EXCHANGE_TOSHO)
	realID := "order-456"
	internalOrder := market.NewOrderPtr(realID, "9434", market.ACTION_BUY, 2000, 100)
	s.Orders = append(s.Orders, internalOrder)

	// 約定が発生した報告が届く
	extOrders := []market.Order{
		{
			ID:     realID,
			Status: market.ORDER_STATUS_IN_PROGRESS,
			CumQty: 50,
			Executions: []market.Execution{
				{ID: "exec-1", Price: 2000, Qty: 50},
			},
		},
	}

	s.SyncOrders(extOrders)

	// ポジションが更新されているか確認
	// ※同じパッケージ内なので s.positions に直接アクセス可能
	holdQty := 0.0
	for _, p := range s.positions {
		holdQty += p.LeavesQty
	}
	if holdQty != 50 {
		t.Errorf("expected holdQty 50, got %f", holdQty)
	}

	// 同じ報告が再度届いても、二重計上されないか確認（冪等性）
	s.SyncOrders(extOrders)
	holdQty = 0.0
	for _, p := range s.positions {
		holdQty += p.LeavesQty
	}
	if holdQty != 50 {
		t.Errorf("expected holdQty 50 (after deduplication), got %f", holdQty)
	}
}
