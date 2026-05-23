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

type MockStrategy struct{}

func (m *MockStrategy) Name() string                                       { return "mock" }
func (m *MockStrategy) Evaluate(input strategy.StrategyInput) brain.Signal { return brain.Signal{} }
func (m *MockStrategy) AnalysisLogger() *slog.Logger                       { return nil }
func (m *MockStrategy) IfDone(input strategy.StrategyInput, prevSignal brain.Signal) brain.Signal {
	return brain.Signal{Action: brain.ACTION_HOLD}
}

type MockDataPool struct {
	tick.DataPool
	Tick tick.Tick
}

func (m *MockDataPool) GetState(symbol string) tick.MarketState {
	return tick.MarketState{
		Symbol:     symbol,
		LatestTick: m.Tick,
	}
}

func TestSniper_SyncOrders(t *testing.T) {
	detail := symbol.Symbol{Code: "9434"}
	policy := &strategy.NoopPolicy{}
	dataPool := &MockDataPool{}
	s := NewSniper(detail, &MockStrategy{}, policy, order.EXCHANGE_TOSHO, nil, dataPool)

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
	dataPool := &MockDataPool{}
	s := NewSniper(detail, &MockStrategy{}, policy, order.EXCHANGE_TOSHO, nil, dataPool)
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

func TestSniper_SyncOrders_CancelSentTimeout(t *testing.T) {
	detail := symbol.Symbol{Code: "9434"}
	policy := &strategy.NoopPolicy{}
	dataPool := &MockDataPool{}
	s := NewSniper(detail, &MockStrategy{}, policy, order.EXCHANGE_TOSHO, nil, dataPool)
	realID := "order-789"
	internalOrder := order.NewOrder(realID, "9434", order.ACTION_BUY, 2000, 100)
	internalOrder.Status = order.ORDER_STATUS_CANCEL_SENT
	s.Orders = append(s.Orders, internalOrder)

	// 1. キャンセル送信直後のため、取引所がまだ IN_PROGRESS と言っても上書きは防ぐ
	internalOrder.CancelSentAt = time.Now()
	extOrders := []order.Order{
		{
			ID:     realID,
			Symbol: "9434",
			Status: order.ORDER_STATUS_IN_PROGRESS,
		},
	}
	s.SyncOrders(order.Orders{Orders: extOrders})
	if internalOrder.Status != order.ORDER_STATUS_CANCEL_SENT {
		t.Errorf("expected status to stay CANCEL_SENT, got %v", internalOrder.Status)
	}

	// 2. 5秒以上経過した後は、取引所のステータス (IN_PROGRESS) で上書きを許可する (キャンセル失敗・却下された時の復帰)
	internalOrder.CancelSentAt = time.Now().Add(-6 * time.Second)
	s.SyncOrders(order.Orders{Orders: extOrders})
	if internalOrder.Status != order.ORDER_STATUS_IN_PROGRESS {
		t.Errorf("expected status to revert to IN_PROGRESS, got %v", internalOrder.Status)
	}
}

func TestSniper_SyncOrders_ChronologicalExecutions(t *testing.T) {
	detail := symbol.Symbol{Code: "9434"}
	policy := &strategy.NoopPolicy{}
	dataPool := &MockDataPool{}
	s := NewSniper(detail, &MockStrategy{}, policy, order.EXCHANGE_TOSHO, nil, dataPool)

	buyOrderID := "buy-order"
	sellOrderID := "sell-order"

	buyOrder := order.NewOrder(buyOrderID, "9434", order.ACTION_BUY, 2000, 100)
	sellOrder := order.NewOrder(sellOrderID, "9434", order.ACTION_SELL, 2010, 100)

	s.Orders = append(s.Orders, buyOrder, sellOrder)

	// API returns newest first: SELL order first, then BUY order
	extOrders := []order.Order{
		{
			ID:     sellOrderID,
			Symbol: "9434",
			Status: order.ORDER_STATUS_FILLED,
			CumQty: 100,
			Executions: []order.Execution{
				{
					ID:            "exec-sell",
					Price:         2010,
					Qty:           100,
					ExecutionTime: time.Now().Add(-1 * time.Second), // Executed 1 second ago
				},
			},
		},
		{
			ID:     buyOrderID,
			Symbol: "9434",
			Status: order.ORDER_STATUS_FILLED,
			CumQty: 100,
			Executions: []order.Execution{
				{
					ID:            "exec-buy",
					Price:         2000,
					Qty:           100,
					ExecutionTime: time.Now().Add(-2 * time.Second), // Executed 2 seconds ago (older!)
				},
			},
		},
	}

	s.SyncOrders(order.Orders{Orders: extOrders})

	// After sync, positions should be empty (0 shares), since we bought 100 and sold 100
	holdQty := 0.0
	for _, p := range s.positions {
		holdQty += p.LeavesQty
	}
	if holdQty != 0 {
		t.Errorf("expected holdQty 0, got %f (positions: %+v)", holdQty, s.positions)
	}
}

func TestSniper_SyncOrders_OrderExecutionSync_AndCleanup(t *testing.T) {
	detail := symbol.Symbol{Code: "9434"}
	policy := &strategy.NoopPolicy{}
	dataPool := &MockDataPool{}
	s := NewSniper(detail, &MockStrategy{}, policy, order.EXCHANGE_TOSHO, nil, dataPool)

	buyOrderID := "buy-order-xyz"
	buyOrder := order.NewOrder(buyOrderID, "9434", order.ACTION_BUY, 2000, 100)
	s.Orders = append(s.Orders, buyOrder)

	// Simulate order is FILLED with an execution of 100 shares
	extOrders := []order.Order{
		{
			ID:     buyOrderID,
			Symbol: "9434",
			Status: order.ORDER_STATUS_FILLED,
			CumQty: 100,
			Executions: []order.Execution{
				{
					ID:            "exec-buy-xyz",
					Price:         2000,
					Qty:           100,
					ExecutionTime: time.Now(),
				},
			},
		},
	}

	s.SyncOrders(order.Orders{Orders: extOrders})

	// 1. Verify internal order's Executions are populated
	if len(buyOrder.Executions) != 1 {
		t.Fatalf("expected 1 execution in internal order, got %d", len(buyOrder.Executions))
	}
	if buyOrder.Executions[0].ID != "exec-buy-xyz" {
		t.Errorf("expected execution ID 'exec-buy-xyz', got '%s'", buyOrder.Executions[0].ID)
	}

	// 2. Verify FilledQty matches CumQty
	if buyOrder.FilledQty() != 100 {
		t.Errorf("expected FilledQty 100, got %f", buyOrder.FilledQty())
	}

	// 3. Verify the completed and fully filled order is successfully cleaned up from s.Orders
	if len(s.Orders) != 0 {
		t.Errorf("expected s.Orders to be empty after filled order cleanup, but got %d orders", len(s.Orders))
	}
}

func TestSniper_ReducePositions_ByID(t *testing.T) {
	detail := symbol.Symbol{Code: "9434"}
	policy := &strategy.NoopPolicy{}
	dataPool := &MockDataPool{}
	s := NewSniper(detail, &MockStrategy{}, policy, order.EXCHANGE_TOSHO, nil, dataPool)

	// Pre-populate with two physical positions
	s.positions = []position.Position{
		{
			ExecutionID: "exec-A",
			Symbol:      "9434",
			LeavesQty:   100,
			Price:       2000,
			Meta:        position.PositionMeta{EntryTime: time.Now().Add(-10 * time.Minute)},
		},
		{
			ExecutionID: "exec-B",
			Symbol:      "9434",
			LeavesQty:   100,
			Price:       2010,
			Meta:        position.PositionMeta{EntryTime: time.Now().Add(-5 * time.Minute)},
		},
	}

	// Create a sell order targeting ONLY Position B (exec-B)
	sellOrderID := "sell-order-B"
	sellOrder := order.NewOrder(sellOrderID, "9434", order.ACTION_SELL, 2020, 100)
	sellOrder.ClosePositions = []order.ClosePosition{
		{HoldID: "exec-B", Qty: 100},
	}
	s.Orders = append(s.Orders, sellOrder)

	// Simulate execution of 100 shares for this sell order
	extOrders := []order.Order{
		{
			ID:     sellOrderID,
			Symbol: "9434",
			Status: order.ORDER_STATUS_FILLED,
			CumQty: 100,
			Executions: []order.Execution{
				{
					ID:            "exec-sell-B",
					Price:         2020,
					Qty:           100,
					ExecutionTime: time.Now(),
				},
			},
		},
	}

	s.SyncOrders(order.Orders{Orders: extOrders})

	// After execution, Position B should be closed, and ONLY Position A should remain!
	if len(s.positions) != 1 {
		t.Fatalf("expected 1 remaining position, got %d", len(s.positions))
	}

	remainingPos := s.positions[0]
	if remainingPos.ExecutionID != "exec-A" {
		t.Errorf("expected remaining position to be 'exec-A', got '%s'", remainingPos.ExecutionID)
	}
	if remainingPos.LeavesQty != 100 || remainingPos.Price != 2000 {
		t.Errorf("expected 'exec-A' to have 100 shares @ 2000, got %f @ %f", remainingPos.LeavesQty, remainingPos.Price)
	}
}

