package strategy_test

import (
	"testing"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/brain"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
)

func TestPosition_Simulate(t *testing.T) {
	// Base position
	pos := strategy.Position{Qty: 10, AveragePrice: 1000.0}

	t.Run("Simulate HOLD", func(t *testing.T) {
		sig := brain.NewHold()
		newPos := pos.Simulate(sig, 1050.0)
		if newPos.Qty != pos.Qty || newPos.AveragePrice != pos.AveragePrice {
			t.Errorf("expected position to be unchanged on HOLD, got %+v", newPos)
		}
	})

	t.Run("Simulate Limit BUY", func(t *testing.T) {
		// Buy 5 units at 1100.0
		// New Qty: 10 + 5 = 15
		// Total Cost: 1000.0 * 10 + 1100.0 * 5 = 10000 + 5500 = 15500
		// New AveragePrice: 15500 / 15 = 1033.333333...
		sig := brain.NewBuyEntry(5, 1100.0, order.ORDER_TYPE_LIMIT, "")
		newPos := pos.Simulate(sig, 1200.0) // tickPrice 1200.0 should be ignored since price > 0
		if newPos.Qty != 15 {
			t.Errorf("expected Qty 15, got %f", newPos.Qty)
		}
		expectedPrice := 15500.0 / 15.0
		if newPos.AveragePrice != expectedPrice {
			t.Errorf("expected AveragePrice %f, got %f", expectedPrice, newPos.AveragePrice)
		}
	})

	t.Run("Simulate Market BUY (price <= 0)", func(t *testing.T) {
		// Buy 10 units at Market (using tickPrice 1050.0)
		// New Qty: 10 + 10 = 20
		// Total Cost: 1000.0 * 10 + 1050.0 * 10 = 10000 + 10500 = 20500
		// New AveragePrice: 20500 / 20 = 1025.0
		sig := brain.NewBuyEntry(10, 0, order.ORDER_TYPE_MARKET, "")
		newPos := pos.Simulate(sig, 1050.0)
		if newPos.Qty != 20 {
			t.Errorf("expected Qty 20, got %f", newPos.Qty)
		}
		if newPos.AveragePrice != 1025.0 {
			t.Errorf("expected AveragePrice 1025.0, got %f", newPos.AveragePrice)
		}
	})

	t.Run("Simulate SELL", func(t *testing.T) {
		// Sell 4 units
		// New Qty: 10 - 4 = 6
		// Average price remains unchanged since it's a sell (closing position)
		sig := brain.NewSellExit(4, 1100.0, order.ORDER_TYPE_LIMIT, "")
		newPos := pos.Simulate(sig, 1200.0)
		if newPos.Qty != 6 {
			t.Errorf("expected Qty 6, got %f", newPos.Qty)
		}
		expectedPrice := 10000.0 / 6.0 // newTotalCost (10000) / newQty (6)
		if newPos.AveragePrice != expectedPrice {
			t.Errorf("expected AveragePrice %f, got %f", expectedPrice, newPos.AveragePrice)
		}
	})

	t.Run("Simulate SELL to zero or negative qty", func(t *testing.T) {
		sig := brain.NewSellExit(10, 1100.0, order.ORDER_TYPE_LIMIT, "")
		newPos := pos.Simulate(sig, 1200.0)
		if newPos.Qty != 0 {
			t.Errorf("expected Qty 0, got %f", newPos.Qty)
		}
		if newPos.AveragePrice != 0.0 {
			t.Errorf("expected AveragePrice 0.0, got %f", newPos.AveragePrice)
		}

		sig2 := brain.NewSellExit(15, 1100.0, order.ORDER_TYPE_LIMIT, "")
		newPos2 := pos.Simulate(sig2, 1200.0)
		if newPos2.Qty != -5 {
			t.Errorf("expected Qty -5, got %f", newPos2.Qty)
		}
		if newPos2.AveragePrice != 0.0 {
			t.Errorf("expected AveragePrice 0.0 for negative qty, got %f", newPos2.AveragePrice)
		}
	})
}

func TestStrategyInput(t *testing.T) {
	pos := strategy.Position{Qty: 12.5, AveragePrice: 1234.5}
	input := strategy.StrategyInput{Position: pos}

	if input.HoldQty() != 12.5 {
		t.Errorf("expected HoldQty 12.5, got %f", input.HoldQty())
	}
	if input.AveragePrice() != 1234.5 {
		t.Errorf("expected AveragePrice 1234.5, got %f", input.AveragePrice())
	}
}
