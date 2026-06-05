package brain_test

import (
	"testing"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/brain"
)

func TestAction_ToMarketAction(t *testing.T) {
	tests := []struct {
		action    brain.Action
		expected  order.Action
		expectErr bool
	}{
		{brain.ACTION_BUY, order.ACTION_BUY, false},
		{brain.ACTION_SELL, order.ACTION_SELL, false},
		{brain.Action("INVALID"), order.Action(""), true},
	}

	for _, tt := range tests {
		t.Run(string(tt.action), func(t *testing.T) {
			got, err := tt.action.ToMarketAction()
			if (err != nil) != tt.expectErr {
				t.Fatalf("unexpected error state: %v", err)
			}
			if !tt.expectErr && got != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, got)
			}
		})
	}
}

func TestNewSignalBuilders(t *testing.T) {
	// 1. NewBuyEntry with default OrderType fallback
	sig1 := brain.NewBuyEntry(10, 2000, 0, "reason1")
	if sig1.Action != brain.ACTION_BUY || sig1.TradeType != brain.TradeEntry || sig1.Quantity != 10 || sig1.Price != 2000 || sig1.OrderType != order.ORDER_TYPE_LIMIT || sig1.Reason != "reason1" {
		t.Errorf("unexpected signal from NewBuyEntry: %+v", sig1)
	}

	// 1b. NewBuyEntry with explicit OrderType
	sig1b := brain.NewBuyEntry(10, 2000, order.ORDER_TYPE_MARKET, "reason1b")
	if sig1b.OrderType != order.ORDER_TYPE_MARKET {
		t.Errorf("expected ORDER_TYPE_MARKET, got %v", sig1b.OrderType)
	}

	// 2. NewSellEntry with default OrderType fallback
	sig2 := brain.NewSellEntry(5, 2010, 0, "reason2")
	if sig2.Action != brain.ACTION_SELL || sig2.TradeType != brain.TradeEntry || sig2.Quantity != 5 || sig2.Price != 2010 || sig2.OrderType != order.ORDER_TYPE_LIMIT || sig2.Reason != "reason2" {
		t.Errorf("unexpected signal from NewSellEntry: %+v", sig2)
	}

	// 3. NewBuyExit with default OrderType fallback
	sig3 := brain.NewBuyExit(8, 1990, 0, "reason3")
	if sig3.Action != brain.ACTION_BUY || sig3.TradeType != brain.TradeExit || sig3.Quantity != 8 || sig3.Price != 1990 || sig3.OrderType != order.ORDER_TYPE_LIMIT || sig3.Reason != "reason3" {
		t.Errorf("unexpected signal from NewBuyExit: %+v", sig3)
	}

	// 3b. NewBuyExit with explicit OrderType
	sig3b := brain.NewBuyExit(8, 1990, order.ORDER_TYPE_MARKET, "reason3b")
	if sig3b.OrderType != order.ORDER_TYPE_MARKET {
		t.Errorf("expected ORDER_TYPE_MARKET, got %v", sig3b.OrderType)
	}

	// 4. NewSellExit with default OrderType fallback
	sig4 := brain.NewSellExit(15, 2005, 0, "reason4")
	if sig4.Action != brain.ACTION_SELL || sig4.TradeType != brain.TradeExit || sig4.Quantity != 15 || sig4.Price != 2005 || sig4.OrderType != order.ORDER_TYPE_LIMIT || sig4.Reason != "reason4" {
		t.Errorf("unexpected signal from NewSellExit: %+v", sig4)
	}

	// 5. NewHold
	sig5 := brain.NewHold()
	if sig5.Action != brain.ACTION_HOLD {
		t.Errorf("unexpected signal from NewHold: %+v", sig5)
	}
}
