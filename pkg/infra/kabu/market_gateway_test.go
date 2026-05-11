package kabu

import (
	"context"
	"testing"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/infra/kabu/api"
)

// MockKabuClient は KabuClient の振る舞いを模倣します
type MockKabuClient struct {
	Orders []api.Order
}

func (m *MockKabuClient) GetOrders() ([]api.Order, error) {
	return m.Orders, nil
}

func (m *MockKabuClient) SendOrder(req api.OrderRequest) (*api.OrderResponse, error) { return nil, nil }
func (m *MockKabuClient) CancelOrder(req api.CancelRequest) (*api.CancelResponse, error) {
	return nil, nil
}
func (m *MockKabuClient) GetPositions(product api.ProductType) ([]api.Position, error) { return nil, nil }
func (m *MockKabuClient) RegisterSymbol(req api.RegisterSymbolRequest) (*api.RegisterSymbolResponse, error) {
	return nil, nil
}
func (m *MockKabuClient) UnregisterSymbolAll() (*api.UnregisterSymbolAllResponse, error) {
	return nil, nil
}
func (m *MockKabuClient) GetSymbol(symbol string, exchange api.ExchageType) (*api.SymbolSuccess, error) {
	return nil, nil
}

func TestMarketGateway_GetOrders(t *testing.T) {
	mockClient := &MockKabuClient{}
	gateway := &MarketGateway{
		client: mockClient,
	}

	// テストケース: 様々な API レスポンスのパターン
	tests := []struct {
		name     string
		apiOrder api.Order
		expected market.OrderStatus
	}{
		{
			name: "State 4: Canceling",
			apiOrder: api.Order{
				ID:    "order-1",
				State: api.STATE_CANCELING,
			},
			expected: market.ORDER_STATUS_CANCEL_SENT,
		},
		{
			name: "State 5 with RecType 6: Canceled",
			apiOrder: api.Order{
				ID:    "order-2",
				State: api.STATE_FINISHED,
				Details: []api.OrderDetail{
					{RecType: api.RECTYPE_CANCELED},
				},
			},
			expected: market.ORDER_STATUS_CANCELED,
		},
		{
			name: "State 5 with Full Fill",
			apiOrder: api.Order{
				ID:       "order-3",
				State:    api.STATE_FINISHED,
				OrderQty: 100,
				CumQty:   100,
			},
			expected: market.ORDER_STATUS_FILLED,
		},
		{
			name: "State 5 with RecType 7: Expired",
			apiOrder: api.Order{
				ID:    "order-4",
				State: api.STATE_FINISHED,
				Details: []api.OrderDetail{
					{RecType: api.RECTYPE_INVALID},
				},
			},
			expected: market.ORDER_STATUS_EXPIRED,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient.Orders = []api.Order{tt.apiOrder}
			orders, err := gateway.GetOrders(context.Background())
			if err != nil {
				t.Fatalf("GetOrders failed: %v", err)
			}

			if len(orders) != 1 {
				t.Fatalf("expected 1 order, got %d", len(orders))
			}

			if orders[0].Status != tt.expected {
				t.Errorf("expected status %d, got %d", tt.expected, orders[0].Status)
			}
		})
	}
}
