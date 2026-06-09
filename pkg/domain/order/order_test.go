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

func TestOrder_Options(t *testing.T) {
	req := &order.OrderRequest{
		Exchange: order.EXCHANGE_TOSHO,
	}
	o := order.NewOrder("test-id", "7203", order.ACTION_BUY, 2000.5, 100,
		order.WithType(order.ORDER_TYPE_MARKET),
		order.WithCashMargin(order.CASH_MARGIN_CASH),
		order.WithRequest(req),
		order.WithReason("TestOption"),
	)

	if o.Type != order.ORDER_TYPE_MARKET {
		t.Errorf("expected Type ORDER_TYPE_MARKET, got %v", o.Type)
	}
	if o.CashMargin != order.CASH_MARGIN_CASH {
		t.Errorf("expected CashMargin CASH_MARGIN_CASH, got %v", o.CashMargin)
	}
	if o.Request != req {
		t.Errorf("expected Request to match, got %v", o.Request)
	}
	if o.Reason != "TestOption" {
		t.Errorf("expected Reason 'TestOption', got '%s'", o.Reason)
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

func TestOrder_AveragePrice_ZeroQty(t *testing.T) {
	o := order.NewOrder("test-id", "7203", order.ACTION_BUY, 2000, 100)
	o.AddExecution(order.Execution{
		ID:    "exec-1",
		Price: 1990,
		Qty:   0,
	})
	if got := o.AveragePrice(); got != 0.0 {
		t.Errorf("expected AveragePrice 0.0 for zero qty execution, got %f", got)
	}
}

func TestOrder_HasExecution_NotFound(t *testing.T) {
	o := order.NewOrder("test-id", "7203", order.ACTION_BUY, 2000, 100)
	if o.HasExecution("non-existent") {
		t.Error("expected HasExecution('non-existent') to be false")
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

func TestOrder_StatusCheckers(t *testing.T) {
	o := order.NewOrder("test", "7203", order.ACTION_BUY, 100, 1)

	tests := []struct {
		status   order.OrderStatus
		checkFn  func(*order.Order) bool
		expected bool
		name     string
	}{
		{order.ORDER_STATUS_WAITING, (*order.Order).IsWaiting, true, "IsWaiting"},
		{order.ORDER_STATUS_IN_PROGRESS, (*order.Order).IsInProgress, true, "IsInProgress"},
		{order.ORDER_STATUS_FILLED, (*order.Order).IsFilled, true, "IsFilled"},
		{order.ORDER_STATUS_CANCELED, (*order.Order).IsCanceled, true, "IsCanceled"},
		{order.ORDER_STATUS_EXPIRED, (*order.Order).IsExpired, true, "IsExpired"},
		{order.ORDER_STATUS_CANCEL_SENT, (*order.Order).IsCancelSent, true, "IsCancelSent"},
		{order.ORDER_STATUS_FILL_EXPECTED, (*order.Order).IsFillExpected, true, "IsFillExpected"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o.BypassTransition(tt.status, o.InternalState())
			if got := tt.checkFn(o); got != tt.expected {
				t.Errorf("%s() for status %v = %v, expected %v", tt.name, tt.status, got, tt.expected)
			}
			o.BypassTransition(order.ORDER_STATUS_NONE, o.InternalState())
			if got := tt.checkFn(o); got == tt.expected {
				t.Errorf("%s() for status ORDER_STATUS_NONE should be false", tt.name)
			}
		})
	}
}

func TestOrder_StatusTransitions(t *testing.T) {
	t.Run("Valid Transitions", func(t *testing.T) {
		o := order.NewOrder("test", "7203", order.ACTION_BUY, 100, 1)

		// ToWaiting
		o.BypassTransition(order.ORDER_STATUS_NONE, o.InternalState())
		o.ToWaiting()
		if o.Status() != order.ORDER_STATUS_WAITING {
			t.Errorf("expected status WAITING, got %v", o.Status())
		}
		o.ToWaiting() // redundant

		// ToInProgress
		o.ToInProgress()
		if o.Status() != order.ORDER_STATUS_IN_PROGRESS {
			t.Errorf("expected status IN_PROGRESS, got %v", o.Status())
		}
		o.ToInProgress() // redundant

		// ToCancelSent
		o.ToCancelSent()
		if o.Status() != order.ORDER_STATUS_CANCEL_SENT {
			t.Errorf("expected status CANCEL_SENT, got %v", o.Status())
		}
		o.ToCancelSent() // redundant

		// ToFillExpected
		o.BypassTransition(order.ORDER_STATUS_WAITING, o.InternalState())
		o.ToFillExpected()
		if o.Status() != order.ORDER_STATUS_FILL_EXPECTED {
			t.Errorf("expected status FILL_EXPECTED, got %v", o.Status())
		}
		o.ToFillExpected() // redundant

		o.ToWaiting()

		// ToFilled
		o.ToFilled()
		if o.Status() != order.ORDER_STATUS_FILLED {
			t.Errorf("expected status FILLED, got %v", o.Status())
		}
		o.ToFilled() // redundant

		// ToCanceled
		o.BypassTransition(order.ORDER_STATUS_IN_PROGRESS, o.InternalState())
		o.ToCanceled()
		if o.Status() != order.ORDER_STATUS_CANCELED {
			t.Errorf("expected status CANCELED, got %v", o.Status())
		}
		o.ToCanceled() // redundant

		// ToExpired
		o.BypassTransition(order.ORDER_STATUS_IN_PROGRESS, o.InternalState())
		o.ToExpired()
		if o.Status() != order.ORDER_STATUS_EXPIRED {
			t.Errorf("expected status EXPIRED, got %v", o.Status())
		}
		o.ToExpired() // redundant
	})

	t.Run("Terminal Panic Status", func(t *testing.T) {
		terminalStatuses := []order.OrderStatus{
			order.ORDER_STATUS_FILLED,
			order.ORDER_STATUS_CANCELED,
			order.ORDER_STATUS_EXPIRED,
		}

		for _, ts := range terminalStatuses {
			t.Run(string(rune(ts)), func(t *testing.T) {
				defer func() {
					if recover() == nil {
						t.Errorf("expected panic when transitioning out of terminal status %v", ts)
					}
				}()
				o := order.NewOrder("test", "7203", order.ACTION_BUY, 100, 1)
				o.BypassTransition(ts, o.InternalState())
				o.ToWaiting()
			})
		}
	})

	t.Run("Invalid Status Transition Panic", func(t *testing.T) {
		invalidCases := []struct {
			from order.OrderStatus
			to   func(*order.Order)
		}{
			{order.ORDER_STATUS_IN_PROGRESS, func(o *order.Order) { o.ToWaiting() }},
			{order.ORDER_STATUS_CANCEL_SENT, func(o *order.Order) { o.ToInProgress() }},
			{order.ORDER_STATUS_FILLED, func(o *order.Order) { o.ToCancelSent() }},
			{order.ORDER_STATUS_CANCELED, func(o *order.Order) { o.ToFillExpected() }},
		}

		for i, tc := range invalidCases {
			t.Run(string(rune(i)), func(t *testing.T) {
				defer func() {
					if recover() == nil {
						t.Errorf("expected panic for invalid transition from status %v", tc.from)
					}
				}()
				o := order.NewOrder("test", "7203", order.ACTION_BUY, 100, 1)
				o.BypassTransition(tc.from, o.InternalState())
				tc.to(o)
			})
		}
	})
}

func TestOrder_InternalStateTransitions(t *testing.T) {
	t.Run("Valid Internal Transitions", func(t *testing.T) {
		o := order.NewOrder("test", "7203", order.ACTION_BUY, 100, 1)

		// ToPending
		o.ToPending()
		if o.InternalState() != order.STATE_PENDING {
			t.Errorf("expected internal state PENDING, got %v", o.InternalState())
		}
		o.ToPending() // redundant

		// ToActive
		o.ToActive()
		if o.InternalState() != order.STATE_ACTIVE {
			t.Errorf("expected internal state ACTIVE, got %v", o.InternalState())
		}
		o.ToActive() // redundant

		// ToCanceling
		o.ToCanceling()
		if o.InternalState() != order.STATE_CANCELING {
			t.Errorf("expected internal state CANCELING, got %v", o.InternalState())
		}
		o.ToCanceling() // redundant

		// ToClosed
		o.ToClosed()
		if o.InternalState() != order.STATE_CLOSED {
			t.Errorf("expected internal state CLOSED, got %v", o.InternalState())
		}
		o.ToClosed() // redundant

		// ToActive directly from PREPARING
		o2 := order.NewOrder("test2", "7203", order.ACTION_BUY, 100, 1)
		o2.ToActive()
		if o2.InternalState() != order.STATE_ACTIVE {
			t.Errorf("expected internal state ACTIVE, got %v", o2.InternalState())
		}
	})

	t.Run("Closed Panic Internal State", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("expected panic when transitioning out of closed internal state")
			}
		}()
		o := order.NewOrder("test", "7203", order.ACTION_BUY, 100, 1)
		o.ToClosed()
		o.ToPending()
	})

	t.Run("Invalid Internal Transition Panic", func(t *testing.T) {
		invalidCases := []struct {
			from order.InternalState
			to   func(*order.Order)
		}{
			{order.STATE_ACTIVE, func(o *order.Order) { o.ToPending() }},
			{order.STATE_PENDING, func(o *order.Order) { o.ToCanceling() }},
			{order.STATE_CANCELING, func(o *order.Order) { o.ToActive() }},
		}

		for i, tc := range invalidCases {
			t.Run(string(rune(i)), func(t *testing.T) {
				defer func() {
					if recover() == nil {
						t.Errorf("expected panic for invalid transition from internal state %v", tc.from)
					}
				}()
				o := order.NewOrder("test", "7203", order.ACTION_BUY, 100, 1)
				o.BypassTransition(o.Status(), tc.from)
				tc.to(o)
			})
		}
	})
}

func TestOrder_TransitionToStatusAndInternalState(t *testing.T) {
	o := order.NewOrder("test", "7203", order.ACTION_BUY, 100, 1)

	// Test TransitionToStatus
	o.TransitionToStatus(order.ORDER_STATUS_WAITING)
	o.TransitionToStatus(order.ORDER_STATUS_IN_PROGRESS)
	if o.Status() != order.ORDER_STATUS_IN_PROGRESS {
		t.Errorf("expected status IN_PROGRESS, got %v", o.Status())
	}

	o.TransitionToStatus(order.ORDER_STATUS_CANCEL_SENT)
	if o.Status() != order.ORDER_STATUS_CANCEL_SENT {
		t.Errorf("expected status CANCEL_SENT, got %v", o.Status())
	}

	o.BypassTransition(order.ORDER_STATUS_NONE, o.InternalState())
	o.TransitionToStatus(order.ORDER_STATUS_FILL_EXPECTED)
	if o.Status() != order.ORDER_STATUS_FILL_EXPECTED {
		t.Errorf("expected status FILL_EXPECTED, got %v", o.Status())
	}

	o.TransitionToStatus(order.ORDER_STATUS_WAITING)
	o.TransitionToStatus(order.ORDER_STATUS_FILLED)
	if o.Status() != order.ORDER_STATUS_FILLED {
		t.Errorf("expected status FILLED, got %v", o.Status())
	}

	o2 := order.NewOrder("test2", "7203", order.ACTION_BUY, 100, 1)
	o2.TransitionToStatus(order.ORDER_STATUS_CANCELED)
	if o2.Status() != order.ORDER_STATUS_CANCELED {
		t.Errorf("expected status CANCELED, got %v", o2.Status())
	}

	o3 := order.NewOrder("test3", "7203", order.ACTION_BUY, 100, 1)
	o3.TransitionToStatus(order.ORDER_STATUS_EXPIRED)
	if o3.Status() != order.ORDER_STATUS_EXPIRED {
		t.Errorf("expected status EXPIRED, got %v", o3.Status())
	}

	// Test TransitionToInternalState
	oInternal := order.NewOrder("test-int", "7203", order.ACTION_BUY, 100, 1)
	oInternal.TransitionToInternalState(order.STATE_PENDING)
	if oInternal.InternalState() != order.STATE_PENDING {
		t.Errorf("expected internal state PENDING, got %v", oInternal.InternalState())
	}
	oInternal.TransitionToInternalState(order.STATE_ACTIVE)
	if oInternal.InternalState() != order.STATE_ACTIVE {
		t.Errorf("expected internal state ACTIVE, got %v", oInternal.InternalState())
	}
	oInternal.TransitionToInternalState(order.STATE_CANCELING)
	if oInternal.InternalState() != order.STATE_CANCELING {
		t.Errorf("expected internal state CANCELING, got %v", oInternal.InternalState())
	}
	oInternal.TransitionToInternalState(order.STATE_CLOSED)
	if oInternal.InternalState() != order.STATE_CLOSED {
		t.Errorf("expected internal state CLOSED, got %v", oInternal.InternalState())
	}
}

func TestAction_ToMarketAction(t *testing.T) {
	tests := []struct {
		action   order.Action
		expected order.Action
		ok       bool
	}{
		{order.ACTION_BUY, order.ACTION_BUY, true},
		{order.ACTION_SELL, order.ACTION_SELL, true},
		{order.Action("INVALID"), order.Action(""), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.action), func(t *testing.T) {
			got, ok := tt.action.ToMarketAction()
			if ok != tt.ok || got != tt.expected {
				t.Errorf("ToMarketAction() for %s = (%s, %v), expected (%s, %v)", tt.action, got, ok, tt.expected, tt.ok)
			}
		})
	}
}

func TestExchangeMarket_String(t *testing.T) {
	tests := []struct {
		market   order.ExchangeMarket
		expected string
	}{
		{order.EXCHANGE_TOSHO, "TOSHO"},
		{order.EXCHANGE_SOR, "SOR"},
		{order.EXCHANGE_TOSHO_PLUS, "TOSHO_PLUS"},
		{order.EXCHANGE_NONE, "NONE"},
		{order.ExchangeMarket(999), "NONE"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.market.String(); got != tt.expected {
				t.Errorf("String() = %s, expected %s", got, tt.expected)
			}
		})
	}
}

func TestExchangeMarket_UnmarshalJSON_Failure(t *testing.T) {
	tests := []string{
		`{invalid json}`,
		`[1, 2, 3]`,
	}

	for _, tt := range tests {
		t.Run(tt, func(t *testing.T) {
			var got order.ExchangeMarket
			if err := json.Unmarshal([]byte(tt), &got); err == nil {
				t.Errorf("expected error unmarshaling %s, got nil", tt)
			}
		})
	}
}

func TestOrder_CanCancel(t *testing.T) {
	tests := []struct {
		name          string
		status        order.OrderStatus
		internalState order.InternalState
		expected      bool
	}{
		{"Waiting & Preparing", order.ORDER_STATUS_WAITING, order.STATE_PREPARING, true},
		{"InProgress & Active", order.ORDER_STATUS_IN_PROGRESS, order.STATE_ACTIVE, true},
		{"Completed Filled", order.ORDER_STATUS_FILLED, order.STATE_CLOSED, false},
		{"Completed Canceled", order.ORDER_STATUS_CANCELED, order.STATE_CLOSED, false},
		{"Completed Expired", order.ORDER_STATUS_EXPIRED, order.STATE_CLOSED, false},
		{"Cancel Sent", order.ORDER_STATUS_CANCEL_SENT, order.STATE_CANCELING, false},
		{"Pending State", order.ORDER_STATUS_WAITING, order.STATE_PENDING, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := order.NewOrder("test", "7203", order.ACTION_BUY, 100, 1)
			o.BypassTransition(tt.status, tt.internalState)
			if got := o.CanCancel(); got != tt.expected {
				t.Errorf("expected CanCancel() = %v, got %v", tt.expected, got)
			}
		})
	}
}

func TestActiveOrders_LockedHoldIDs(t *testing.T) {
	// 1. Nil element check
	var aos order.ActiveOrders = []*order.Order{nil}
	locked := aos.LockedHoldIDs()
	if len(locked) != 0 {
		t.Errorf("expected empty map for nil order, got %v", locked)
	}

	// 2. Active exit order (not completed, not cancel sent, margin exit, request present)
	o1 := order.NewOrder("o1", "7203", order.ACTION_SELL, 100, 1)
	o1.BypassTransition(order.ORDER_STATUS_IN_PROGRESS, order.STATE_ACTIVE)
	o1.CashMargin = order.CASH_MARGIN_MARGIN_EXIT
	o1.Request = &order.OrderRequest{
		ClosePositions: []order.ClosePosition{
			{HoldID: "hold-1", Qty: 1},
			{HoldID: "hold-2", Qty: 1},
		},
	}

	// 3. Completed or CancelSent exit order (should NOT lock)
	o2 := order.NewOrder("o2", "7203", order.ACTION_SELL, 100, 1)
	o2.BypassTransition(order.ORDER_STATUS_FILLED, order.STATE_CLOSED)
	o2.CashMargin = order.CASH_MARGIN_MARGIN_EXIT
	o2.Request = &order.OrderRequest{
		ClosePositions: []order.ClosePosition{
			{HoldID: "hold-completed", Qty: 1},
		},
	}

	o3 := order.NewOrder("o3", "7203", order.ACTION_SELL, 100, 1)
	o3.BypassTransition(order.ORDER_STATUS_CANCEL_SENT, order.STATE_CANCELING)
	o3.CashMargin = order.CASH_MARGIN_MARGIN_EXIT
	o3.Request = &order.OrderRequest{
		ClosePositions: []order.ClosePosition{
			{HoldID: "hold-cancelsent", Qty: 1},
		},
	}

	// 4. Parent order with IfDone exit order (in-flight exit tracked via IfDone and Executions)
	oParent := order.NewOrder("oParent", "7203", order.ACTION_BUY, 100, 1)
	oChild := order.NewOrder("oChild", "7203", order.ACTION_SELL, 100, 1)
	oChild.CashMargin = order.CASH_MARGIN_MARGIN_EXIT
	oParent.IfDone = oChild
	oParent.AddExecution(order.Execution{ID: "hold-ifdone-exec", Price: 100, Qty: 1})

	aos = order.ActiveOrders{o1, o2, o3, oParent}
	locked = aos.LockedHoldIDs()

	if !locked["hold-1"] || !locked["hold-2"] {
		t.Errorf("expected hold-1 and hold-2 to be locked, got %v", locked)
	}
	if locked["hold-completed"] {
		t.Errorf("completed order should not lock hold ID")
	}
	if locked["hold-cancelsent"] {
		t.Errorf("cancel sent order should not lock hold ID")
	}
	if !locked["hold-ifdone-exec"] {
		t.Errorf("expected hold-ifdone-exec to be locked via parent execution")
	}
}

func TestOrder_Transitions_ExtraPanics(t *testing.T) {
	t.Run("ToCancelSent invalid state panic", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("expected panic when transitioning to CancelSent from invalid non-terminal state")
			}
		}()
		o := order.NewOrder("test", "7203", order.ACTION_BUY, 100, 1)
		// Use an arbitrary invalid non-terminal state
		o.BypassTransition(order.OrderStatus(99), order.STATE_PREPARING)
		o.ToCancelSent()
	})

	t.Run("ToFillExpected invalid state panic", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("expected panic when transitioning to FillExpected from CancelSent")
			}
		}()
		o := order.NewOrder("test", "7203", order.ACTION_BUY, 100, 1)
		o.BypassTransition(order.ORDER_STATUS_CANCEL_SENT, order.STATE_CANCELING)
		o.ToFillExpected()
	})
}

