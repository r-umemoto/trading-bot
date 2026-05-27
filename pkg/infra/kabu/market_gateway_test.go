package kabu

import (
	"context"
	"testing"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/infra/kabu/api"
)

// MockKabuClient は KabuClient の振る舞いを模倣します
type MockKabuClient struct {
	Orders          []api.Order
	LastSendRequest *api.OrderRequest
}

func (m *MockKabuClient) GetOrders() ([]api.Order, error) {
	return m.Orders, nil
}

func (m *MockKabuClient) SendOrder(req api.OrderRequest) (*api.OrderResponse, error) {
	m.LastSendRequest = &req
	return &api.OrderResponse{OrderId: "test-order-id"}, nil
}
func (m *MockKabuClient) CancelOrder(req api.CancelRequest) (*api.CancelResponse, error) {
	return nil, nil
}
func (m *MockKabuClient) GetPositions(product api.ProductType) ([]api.Position, error) {
	return nil, nil
}
func (m *MockKabuClient) RegisterSymbol(req api.RegisterSymbolRequest) (*api.RegisterSymbolResponse, error) {
	return nil, nil
}
func (m *MockKabuClient) UnregisterSymbolAll() (*api.UnregisterSymbolAllResponse, error) {
	return nil, nil
}
func (m *MockKabuClient) GetSymbol(symbol string, exchange api.ExchageType) (*api.SymbolSuccess, error) {
	return nil, nil
}
func (m *MockKabuClient) GetBoard(symbol string) (*api.BoardResponse, error) {
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
		expected order.OrderStatus
	}{
		{
			name: "State 4: Canceling",
			apiOrder: api.Order{
				ID:    "order-1",
				State: api.STATE_CANCELING,
			},
			expected: order.ORDER_STATUS_CANCEL_SENT,
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
			expected: order.ORDER_STATUS_CANCELED,
		},
		{
			name: "State 5 with Full Fill",
			apiOrder: api.Order{
				ID:       "order-3",
				State:    api.STATE_FINISHED,
				OrderQty: 100,
				CumQty:   100,
			},
			expected: order.ORDER_STATUS_FILLED,
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
			expected: order.ORDER_STATUS_EXPIRED,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient.Orders = []api.Order{tt.apiOrder}
			ords, err := gateway.GetOrders(context.Background())
			if err != nil {
				t.Fatalf("GetOrders failed: %v", err)
			}

			if len(ords.Orders) != 1 {
				t.Fatalf("expected 1 order, got %d", len(ords.Orders))
			}

			if ords.Orders[0].Status != tt.expected {
				t.Errorf("expected status %d, got %d", tt.expected, ords.Orders[0].Status)
			}
		})
	}
}
func TestMarketGateway_SendOrderRaw_DelivType(t *testing.T) {
	mockClient := &MockKabuClient{}
	gateway := &MarketGateway{
		client: mockClient,
	}

	tests := []struct {
		name             string
		cashMargin       order.CashMarginType
		action           order.Action
		closePosOrder    order.ClosePositionOrder
		closePos         []order.ClosePosition
		expectedDelivType int32
	}{
		{
			name:             "現物買 -> DelivType=2",
			cashMargin:       order.CASH_MARGIN_CASH,
			action:           order.ACTION_BUY,
			expectedDelivType: 2,
		},
		{
			name:             "現物売 -> DelivType=0",
			cashMargin:       order.CASH_MARGIN_CASH,
			action:           order.ACTION_SELL,
			expectedDelivType: 0,
		},
		{
			name:             "信用新規 -> DelivType=0",
			cashMargin:       order.CASH_MARGIN_MARGIN_ENTRY,
			action:           order.ACTION_BUY,
			expectedDelivType: 0,
		},
		{
			name:             "信用返済 (ClosePositionOrder指定) -> DelivType=2",
			cashMargin:       order.CASH_MARGIN_MARGIN_EXIT,
			action:           order.ACTION_SELL,
			closePosOrder:    order.CLOSE_POSITION_ASC_DAY_DEC_PL,
			expectedDelivType: 2,
		},
		{
			name:             "信用返済 (ClosePositions指定) -> DelivType=2",
			cashMargin:       order.CASH_MARGIN_MARGIN_EXIT,
			action:           order.ACTION_SELL,
			closePos:         []order.ClosePosition{{HoldID: "test-id", Qty: 100}},
			expectedDelivType: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ord := order.NewOrder("test-local-id", "8801", tt.action, 1000, 100)
			ord.CashMargin = tt.cashMargin

			req := order.NewOrderRequest(
				order.EXCHANGE_TOSHO,
				order.SECURITY_TYPE_STOCK,
				order.TRADE_TYPE_SYSTEM,
				order.ACCOUNT_SPECIAL,
				tt.closePosOrder,
				tt.closePos,
				order.ORDER_TYPE_LIMIT,
			)

			_, err := gateway.SendOrderRaw(context.Background(), order.SendOrderInput{
				Order:   *ord,
				Request: req,
			})

			if err != nil {
				t.Fatalf("SendOrderRaw failed: %v", err)
			}

			if mockClient.LastSendRequest == nil {
				t.Fatal("API request was not captured by mock client")
			}

			if mockClient.LastSendRequest.DelivType != tt.expectedDelivType {
				t.Errorf("expected DelivType %d, got %d", tt.expectedDelivType, mockClient.LastSendRequest.DelivType)
			}
		})
	}
}
