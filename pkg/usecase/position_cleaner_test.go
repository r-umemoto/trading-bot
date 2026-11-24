package usecase

import (
	"context"
	"testing"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/position"
)

type mockCleanableTarget struct {
	forceExitCalled bool
	symbolCode      string
	activeOrders    []*order.Order
}

func (m *mockCleanableTarget) ForceExit() {
	m.forceExitCalled = true
}

func (m *mockCleanableTarget) GetSymbolCode() string {
	return m.symbolCode
}

func (m *mockCleanableTarget) GetActiveOrders() []*order.Order {
	return m.activeOrders
}

type mockGateway struct {
	market.MarketGateway
	cancelCalledWith []string
	cancelErr        error
	positionsQueue   [][]position.Position
	getPosCallCount  int
	sentOrders       []*order.Order
}

func (m *mockGateway) CancelOrder(ctx context.Context, orderID string) error {
	m.cancelCalledWith = append(m.cancelCalledWith, orderID)
	return m.cancelErr
}

func (m *mockGateway) GetPositions(ctx context.Context, product order.ProductType) ([]position.Position, error) {
	if len(m.positionsQueue) > 0 {
		idx := m.getPosCallCount
		m.getPosCallCount++
		if idx < len(m.positionsQueue) {
			return m.positionsQueue[idx], nil
		}
		return nil, nil
	}
	// Return empty positions to terminate CleanAllPositions loop early without retrying/waiting
	return nil, nil
}

func (m *mockGateway) SendOrder(ctx context.Context, input order.SendOrderInput) (*order.Order, error) {
	m.sentOrders = append(m.sentOrders, input.Order)
	return input.Order, nil
}

func TestPositionCleaner_CleanAllPositions(t *testing.T) {
	// 1. Arrange: set up mock cleanable target with active orders
	o1 := order.NewOrder("order-1", "7203", order.ACTION_BUY, 2000, 100)
	o1.BypassTransition(order.ORDER_STATUS_IN_PROGRESS, order.STATE_ACTIVE)

	o2 := order.NewOrder("local-2", "7203", order.ACTION_BUY, 2000, 100)
	// local-2 is pending (has not been sent to API yet)
	// o2.InternalState defaults to STATE_PREPARING, status defaults to ORDER_STATUS_WAITING

	target := &mockCleanableTarget{
		symbolCode:   "7203",
		activeOrders: []*order.Order{o1, o2},
	}

	gateway := &mockGateway{}

	cleaner := NewPositionCleaner([]CleanableTarget{target}, gateway)

	// 2. Act: call CleanAllPositions
	err := cleaner.CleanAllPositions(context.Background())
	if err != nil {
		t.Fatalf("CleanAllPositions failed: %v", err)
	}

	// 3. Assert:
	// - target.ForceExit should be called
	if !target.forceExitCalled {
		t.Errorf("expected target.ForceExit to be called")
	}

	// - o1 (sent order) should transition to CANCEL_SENT
	if o1.Status() != order.ORDER_STATUS_CANCEL_SENT {
		t.Errorf("expected o1 status to transition to CANCEL_SENT, got %v", o1.Status())
	}
	if o1.CancelSentAt.IsZero() {
		t.Errorf("expected o1.CancelSentAt to be set")
	}

	// - o2 (pending order) should transition to CANCELED and STATE_CLOSED locally
	if o2.Status() != order.ORDER_STATUS_CANCELED {
		t.Errorf("expected o2 status to transition to CANCELED, got %v", o2.Status())
	}
	if o2.InternalState() != order.STATE_CLOSED {
		t.Errorf("expected o2 internal state to transition to STATE_CLOSED, got %v", o2.InternalState())
	}

	// - gateway.CancelOrder should be called for o1
	if len(gateway.cancelCalledWith) != 1 || gateway.cancelCalledWith[0] != "order-1" {
		t.Errorf("expected gateway.CancelOrder to be called once with 'order-1', got %v", gateway.cancelCalledWith)
	}

	// - Calling CleanAllPositions a second time (e.g. ForceExit followed by Shutdown) should not re-cancel CANCEL_SENT orders
	if err := cleaner.CleanAllPositions(context.Background()); err != nil {
		t.Fatalf("second CleanAllPositions failed: %v", err)
	}
	if len(gateway.cancelCalledWith) != 1 {
		t.Errorf("expected no duplicate CancelOrder calls on second CleanAllPositions, got %v", gateway.cancelCalledWith)
	}
}

func TestPositionCleaner_BuildForceExitOrder_SetsMarginExitAndHoldID(t *testing.T) {
	pos := position.Position{
		ExecutionID: "E_20261125_001",
		Symbol:      "7203",
		Exchange:    order.EXCHANGE_TOSHO,
		Action:      order.ACTION_BUY,
		TradeType:   order.TRADE_TYPE_GENERAL_DAY,
		AccountType: order.ACCOUNT_SPECIAL,
		LeavesQty:   100,
	}

	ord := buildForceExitOrder(pos)
	if ord.CashMargin != order.CASH_MARGIN_MARGIN_EXIT {
		t.Errorf("expected CASH_MARGIN_MARGIN_EXIT, got %v", ord.CashMargin)
	}
	if ord.Type != order.ORDER_TYPE_MARKET {
		t.Errorf("expected ORDER_TYPE_MARKET, got %v", ord.Type)
	}
	if ord.Action != order.ACTION_SELL {
		t.Errorf("expected ACTION_SELL for closing BUY position, got %v", ord.Action)
	}
	if ord.Request == nil || len(ord.Request.ClosePositions) != 1 {
		t.Fatalf("expected 1 ClosePosition, got %+v", ord.Request)
	}
	if ord.Request.ClosePositions[0].HoldID != "E_20261125_001" || ord.Request.ClosePositions[0].Qty != 100 {
		t.Errorf("unexpected ClosePosition: %+v", ord.Request.ClosePositions[0])
	}
}
