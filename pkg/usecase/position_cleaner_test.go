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
	cancelCalledWith string
	cancelErr        error
}

func (m *mockGateway) CancelOrder(ctx context.Context, orderID string) error {
	m.cancelCalledWith = orderID
	return m.cancelErr
}

func (m *mockGateway) GetPositions(ctx context.Context, product order.ProductType) ([]position.Position, error) {
	// Return empty positions to terminate CleanAllPositions loop early without retrying/waiting
	return nil, nil
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
	if gateway.cancelCalledWith != "order-1" {
		t.Errorf("expected gateway.CancelOrder to be called with 'order-1', got %q", gateway.cancelCalledWith)
	}
}
