package service

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/brain"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
)

type mockStrategy struct{}

func (m *mockStrategy) Name() string                                       { return "mock" }
func (m *mockStrategy) Evaluate(input strategy.StrategyInput) brain.Signal { return brain.Signal{} }
func (m *mockStrategy) AnalysisLogger() *slog.Logger                       { return nil }
func (m *mockStrategy) IfDone(input strategy.StrategyInput, prevSignal brain.Signal) brain.Signal {
	return brain.Signal{Action: brain.ACTION_HOLD}
}

type MockMarketGateway struct {
	market.MarketGateway
	CancelOrderFunc func(ctx context.Context, orderID string) error
	SendOrderFunc   func(ctx context.Context, input order.SendOrderInput) (order.Order, error)
}

func (m *MockMarketGateway) CancelOrder(ctx context.Context, orderID string) error {
	if m.CancelOrderFunc != nil {
		return m.CancelOrderFunc(ctx, orderID)
	}
	return nil
}

func (m *MockMarketGateway) SendOrder(ctx context.Context, input order.SendOrderInput) (order.Order, error) {
	if m.SendOrderFunc != nil {
		return m.SendOrderFunc(ctx, input)
	}
	return order.Order{}, nil
}

func TestOrderDispatcher_Submit_OverrideAndHoldCleanup(t *testing.T) {
	gateway := &MockMarketGateway{}
	dispatcher := NewOrderDispatcher(gateway)

	detail := symbol.Symbol{Code: "9434"}
	policy := &strategy.NoopPolicy{}
	s := sniper.NewSniper(detail, &mockStrategy{}, policy, order.EXCHANGE_TOSHO, nil)

	// 1. Create a pending order and submit it
	order1 := order.NewOrder(order.GenerateLocalID(), "9434", order.ACTION_BUY, 2000, 100)
	s.ManagedOrders = append(s.ManagedOrders, sniper.NewManagedOrder(order1.ID, order1, nil))

	bullet1 := sniper.Bullet{
		Order:   order1,
		Request: &order.OrderRequest{},
	}
	dispatcher.Submit(s, bullet1)

	if len(dispatcher.pendingJobs) != 1 {
		t.Errorf("expected 1 pending job, got %d", len(dispatcher.pendingJobs))
	}

	// 2. Submit a new order for the same symbol (override)
	order2 := order.NewOrder(order.GenerateLocalID(), "9434", order.ACTION_BUY, 2010, 100)
	s.ManagedOrders = append(s.ManagedOrders, sniper.NewManagedOrder(order2.ID, order2, nil))

	bullet2 := sniper.Bullet{
		Order:   order2,
		Request: &order.OrderRequest{},
	}
	dispatcher.Submit(s, bullet2)

	// Verify order1 is removed from s.ManagedOrders (FailSendingOrder should be called on it)
	foundOrder1 := false
	for _, m := range s.ManagedOrders {
		if m.Entry == order1 || m.Exit == order1 {
			foundOrder1 = true
		}
	}
	if foundOrder1 {
		t.Error("expected order1 to be removed from s.ManagedOrders after override")
	}

	// 3. Submit a HOLD bullet (no order and no cancel)
	bullet3 := sniper.Bullet{}
	dispatcher.Submit(s, bullet3)

	// Verify order2 is also removed from s.ManagedOrders on HOLD cleanup
	foundOrder2 := false
	for _, m := range s.ManagedOrders {
		if m.Entry == order2 || m.Exit == order2 {
			foundOrder2 = true
		}
	}
	if foundOrder2 {
		t.Error("expected order2 to be removed from s.ManagedOrders after HOLD cleanup")
	}

	if len(dispatcher.pendingJobs) != 0 {
		t.Errorf("expected pending jobs to be empty, got %d", len(dispatcher.pendingJobs))
	}
}

func TestOrderDispatcher_CancelFailureRevert(t *testing.T) {
	gateway := &MockMarketGateway{
		CancelOrderFunc: func(ctx context.Context, orderID string) error {
			return errors.New("API simulation error (500)")
		},
	}
	dispatcher := NewOrderDispatcher(gateway)

	detail := symbol.Symbol{Code: "9434"}
	policy := &strategy.NoopPolicy{}
	s := sniper.NewSniper(detail, &mockStrategy{}, policy, order.EXCHANGE_TOSHO, nil)

	// Create an order in CANCEL_SENT status
	ord := order.NewOrder("order-789", "9434", order.ACTION_BUY, 2000, 100)
	ord.Status = order.ORDER_STATUS_CANCEL_SENT
	s.ManagedOrders = append(s.ManagedOrders, sniper.NewManagedOrder(ord.ID, ord, nil))

	job := &OrderJob{
		Symbol:   "9434",
		Sniper:   s,
		CancelID: "order-789",
		OrderPtr: ord,
		Priority: 10,
	}

	// Run executeJob which will try to cancel and fail
	dispatcher.executeJob(context.Background(), job)

	// Verify the status of the order is reverted back to IN_PROGRESS
	if ord.Status != order.ORDER_STATUS_IN_PROGRESS {
		t.Errorf("expected order status to be reverted to IN_PROGRESS(2), got %v", ord.Status)
	}
}
