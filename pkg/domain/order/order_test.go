package order_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
)

func TestNewOrder(t *testing.T) {
	o := order.NewOrder("test-id", "7203", order.ACTION_BUY, 2000.5, 100)

	if o.ID != "test-id" {
		t.Errorf("expected ID 'test-id', got '%s'", o.ID)
	}
	if o.Symbol != "7203" {
		t.Errorf("expected Symbol '7203', got '%s'", o.Symbol)
	}
	if o.Action != order.ACTION_BUY {
		t.Errorf("expected Action 'BUY', got '%s'", o.Action)
	}
	if o.OrderPrice != 2000.5 {
		t.Errorf("expected OrderPrice 2000.5, got %f", o.OrderPrice)
	}
	if o.OrderQty != 100 {
		t.Errorf("expected OrderQty 100, got %f", o.OrderQty)
	}
	if o.Status() != order.ORDER_STATUS_WAITING {
		t.Errorf("expected Status ORDER_STATUS_WAITING, got %v", o.Status())
	}
	if o.InternalState() != order.STATE_PREPARING {
		t.Errorf("expected InternalState STATE_PREPARING, got %v", o.InternalState())
	}
}

func TestOrder_IsPending(t *testing.T) {
	tests := []struct {
		state    order.InternalState
		expected bool
	}{
		{order.STATE_PREPARING, true},
		{order.STATE_PENDING, true},
		{order.STATE_ACTIVE, false},
		{order.STATE_CANCELING, false},
		{order.STATE_CLOSED, false},
	}

	for _, tt := range tests {
		t.Run(string(rune(tt.state)), func(t *testing.T) {
			o := order.NewOrder("test", "7203", order.ACTION_BUY, 100, 1)
			o.BypassTransition(o.Status(), tt.state)
			if got := o.IsPending(); got != tt.expected {
				t.Errorf("IsPending() for state %v = %v, expected %v", tt.state, got, tt.expected)
			}
		})
	}
}

func TestOrder_ExecutionsAndPrice(t *testing.T) {
	o := order.NewOrder("test-id", "7203", order.ACTION_BUY, 2000, 100)

	// 最初は0
	if got := o.FilledQty(); got != 0 {
		t.Errorf("expected FilledQty 0, got %f", got)
	}
	if got := o.AveragePrice(); got != 0.0 {
		t.Errorf("expected AveragePrice 0.0, got %f", got)
	}

	// 1つ目の約定
	exec1 := order.Execution{
		ID:    "exec-1",
		Price: 1990,
		Qty:   40,
	}
	o.AddExecution(exec1)

	if got := o.FilledQty(); got != 40 {
		t.Errorf("expected FilledQty 40, got %f", got)
	}
	if got := o.AveragePrice(); got != 1990.0 {
		t.Errorf("expected AveragePrice 1990.0, got %f", got)
	}
	if !o.HasExecution("exec-1") {
		t.Error("expected HasExecution('exec-1') to be true")
	}

	// 重複約定の追加（冪等性の確認）
	o.AddExecution(exec1)
	if got := o.FilledQty(); got != 40 {
		t.Errorf("expected FilledQty 40 (unchanged), got %f", got)
	}

	// 2つ目の約定
	exec2 := order.Execution{
		ID:    "exec-2",
		Price: 2010,
		Qty:   60,
	}
	o.AddExecution(exec2)

	// 合計数量: 40 + 60 = 100
	// 平均価格: (1990*40 + 2010*60) / 100 = (79600 + 120600) / 100 = 200200 / 100 = 2002
	if got := o.FilledQty(); got != 100 {
		t.Errorf("expected FilledQty 100, got %f", got)
	}
	if got := o.AveragePrice(); got != 2002.0 {
		t.Errorf("expected AveragePrice 2002.0, got %f", got)
	}
}

func TestOrder_IsCompleted(t *testing.T) {
	tests := []struct {
		status   order.OrderStatus
		expected bool
	}{
		{order.ORDER_STATUS_NONE, false},
		{order.ORDER_STATUS_WAITING, false},
		{order.ORDER_STATUS_IN_PROGRESS, false},
		{order.ORDER_STATUS_FILLED, true},
		{order.ORDER_STATUS_CANCELED, true},
		{order.ORDER_STATUS_EXPIRED, true},
		{order.ORDER_STATUS_CANCEL_SENT, false},
		{order.ORDER_STATUS_FILL_EXPECTED, false},
	}

	for _, tt := range tests {
		t.Run(string(rune(tt.status)), func(t *testing.T) {
			o := order.NewOrder("test", "7203", order.ACTION_BUY, 100, 1)
			o.BypassTransition(tt.status, o.InternalState())
			if got := o.IsCompleted(); got != tt.expected {
				t.Errorf("IsCompleted() for status %v = %v, expected %v", tt.status, got, tt.expected)
			}
		})
	}
}

func TestGenerateLocalID(t *testing.T) {
	id := order.GenerateLocalID()
	if !strings.HasPrefix(id, "local-") {
		t.Errorf("expected GenerateLocalID to start with 'local-', got '%s'", id)
	}
}

func TestOrder_GetCancelTimeout(t *testing.T) {
	tests := []struct {
		reason   string
		expected time.Duration
	}{
		{"ForceExit", 2 * time.Second},
		{"PairExit", 2 * time.Second},
		{"ExitTrigger", 2 * time.Second},
		{"ImmediateForce", 2 * time.Second},
		{"EntryLimit", 10 * time.Second},
		{"BBReversion", 10 * time.Second},
		{"", 10 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			o := &order.Order{Reason: tt.reason}
			if got := o.GetCancelTimeout(); got != tt.expected {
				t.Errorf("GetCancelTimeout() for reason '%s' = %v, expected %v", tt.reason, got, tt.expected)
			}
		})
	}
}

func TestExchangeMarket_JSON(t *testing.T) {
	// Unmarshal テスト
	tests := []struct {
		input    string
		expected order.ExchangeMarket
	}{
		{`"TOSHO"`, order.EXCHANGE_TOSHO},
		{`"SOR"`, order.EXCHANGE_SOR},
		{`"TOSHO_PLUS"`, order.EXCHANGE_TOSHO_PLUS},
		{`"UNKNOWN"`, order.EXCHANGE_NONE},
		{`3`, order.EXCHANGE_TOSHO_PLUS}, // 数値形式フォールバック
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			var got order.ExchangeMarket
			if err := json.Unmarshal([]byte(tt.input), &got); err != nil {
				t.Fatalf("failed to unmarshal JSON: %v", err)
			}
			if got != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, got)
			}
		})
	}

	// Marshal テスト
	mVal := order.EXCHANGE_SOR
	data, err := json.Marshal(mVal)
	if err != nil {
		t.Fatalf("failed to marshal JSON: %v", err)
	}
	if string(data) != `"SOR"` {
		t.Errorf("expected '\"SOR\"', got '%s'", string(data))
	}
}
