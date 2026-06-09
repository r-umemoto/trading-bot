package strategy_test

import (
	"testing"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/brain"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

func TestTouchTTLPolicy_ApplySyntheticFill(t *testing.T) {
	policy := &strategy.TouchTTLPolicy{TTL: 1 * time.Second}
	now := time.Now()

	t.Run("Early returns", func(t *testing.T) {
		ord := order.NewOrder("test", "7203", order.ACTION_BUY, 2000, 100)
		ord.ToCancelSent()

		// CancelSent order should not trigger fill expected
		policy.ApplySyntheticFill(ord, tick.Tick{Price: 1999})
		if ord.IsFillExpected() {
			t.Error("expected CancelSent order not to trigger fill expected")
		}

		ord2 := order.NewOrder("test2", "7203", order.ACTION_BUY, 2000, 100)
		ord2.ToInProgress()
		ord2.ToFilled()

		// Completed order should not trigger fill expected
		policy.ApplySyntheticFill(ord2, tick.Tick{Price: 1999})
		if ord2.IsWaiting() { // should not change out of terminal state
			t.Error("expected completed order to remain unchanged")
		}
	})

	t.Run("Invalid prices", func(t *testing.T) {
		// Zero order price or zero tick price should do nothing
		ord := order.NewOrder("test", "7203", order.ACTION_BUY, 0, 100)
		policy.ApplySyntheticFill(ord, tick.Tick{Price: 2000})
		if ord.IsFillExpected() {
			t.Error("expected zero order price not to trigger fill expected")
		}

		ord2 := order.NewOrder("test", "7203", order.ACTION_BUY, 2000, 100)
		policy.ApplySyntheticFill(ord2, tick.Tick{Price: 0})
		if ord2.IsFillExpected() {
			t.Error("expected zero tick price not to trigger fill expected")
		}
	})

	t.Run("Buy Touch and Timeout", func(t *testing.T) {
		ord := order.NewOrder("test", "7203", order.ACTION_BUY, 2000, 100)
		ord.ToInProgress()

		// Touch (tick <= ord.OrderPrice)
		policy.ApplySyntheticFill(ord, tick.Tick{Price: 2000, CurrentPriceTime: now})
		if !ord.IsFillExpected() {
			t.Error("expected order to be FillExpected on touch")
		}
		if ord.Synthetic.ExpectedAt != now {
			t.Errorf("expected ExpectedAt to be %v, got %v", now, ord.Synthetic.ExpectedAt)
		}

		// Subsequent touch before TTL expires should remain FillExpected
		policy.ApplySyntheticFill(ord, tick.Tick{Price: 2000, CurrentPriceTime: now.Add(500 * time.Millisecond)})
		if !ord.IsFillExpected() {
			t.Error("expected order to remain FillExpected before TTL")
		}

		// Touch after TTL expires should timeout and revert to Waiting
		policy.ApplySyntheticFill(ord, tick.Tick{Price: 2000, CurrentPriceTime: now.Add(1500 * time.Millisecond)})
		if !ord.IsWaiting() {
			t.Errorf("expected order to revert to Waiting on timeout, got %v", ord.Status())
		}
		if !ord.Synthetic.TouchTimeout {
			t.Error("expected TouchTimeout to be true")
		}

		// Re-touch during TouchTimeout and same price should not trigger expected again
		policy.ApplySyntheticFill(ord, tick.Tick{Price: 2000, CurrentPriceTime: now.Add(2000 * time.Millisecond)})
		if ord.IsFillExpected() {
			t.Error("expected order not to trigger FillExpected again while TouchTimeout is true")
		}

		// Price moves away (tick > ord.OrderPrice for buy) -> reset TouchTimeout
		policy.ApplySyntheticFill(ord, tick.Tick{Price: 2001, CurrentPriceTime: now.Add(2500 * time.Millisecond)})
		if ord.Synthetic.TouchTimeout {
			t.Error("expected TouchTimeout to be reset when price moves away")
		}

		// Touch again with zero price time (triggers fallback to time.Now)
		policy.ApplySyntheticFill(ord, tick.Tick{Price: 2000})
		if !ord.IsFillExpected() {
			t.Error("expected order to be FillExpected again after reset")
		}
	})

	t.Run("Sell Touch and Timeout", func(t *testing.T) {
		ord := order.NewOrder("test", "7203", order.ACTION_SELL, 2000, 100)
		ord.ToInProgress()

		// Touch (tick >= ord.OrderPrice)
		policy.ApplySyntheticFill(ord, tick.Tick{Price: 2000, CurrentPriceTime: now})
		if !ord.IsFillExpected() {
			t.Error("expected order to be FillExpected on touch")
		}

		// Price moves away (tick < ord.OrderPrice for sell) -> reset
		policy.ApplySyntheticFill(ord, tick.Tick{Price: 1999, CurrentPriceTime: now})
		if ord.IsFillExpected() {
			t.Error("expected FillExpected to be reset when price moves away")
		}
	})
}

func TestStrictPiercePolicy_ApplySyntheticFill(t *testing.T) {
	policy := &strategy.StrictPiercePolicy{}

	t.Run("Early returns and invalid prices", func(t *testing.T) {
		ord := order.NewOrder("test", "7203", order.ACTION_BUY, 2000, 100)
		ord.ToCancelSent()
		policy.ApplySyntheticFill(ord, tick.Tick{Price: 1999})
		if ord.IsFillExpected() {
			t.Error("expected cancel sent order to be ignored")
		}

		ord2 := order.NewOrder("test", "7203", order.ACTION_BUY, 0, 100)
		policy.ApplySyntheticFill(ord2, tick.Tick{Price: 2000})
		if ord2.IsFillExpected() {
			t.Error("expected zero price order to be ignored")
		}
	})

	t.Run("Buy Pierce", func(t *testing.T) {
		ord := order.NewOrder("test", "7203", order.ACTION_BUY, 2000, 100)
		ord.ToInProgress()

		// Touch only (price == ord.OrderPrice) -> should NOT fill expected
		policy.ApplySyntheticFill(ord, tick.Tick{Price: 2000})
		if ord.IsFillExpected() {
			t.Error("expected touch only not to trigger Pierce fill expected")
		}

		// Pierce (price < ord.OrderPrice) -> should fill expected
		policy.ApplySyntheticFill(ord, tick.Tick{Price: 1999})
		if !ord.IsFillExpected() {
			t.Error("expected pierce to trigger fill expected")
		}

		// Move away (price >= ord.OrderPrice) -> reset
		policy.ApplySyntheticFill(ord, tick.Tick{Price: 2000})
		if ord.IsFillExpected() {
			t.Error("expected fill expected to reset when price is no longer pierced")
		}
	})

	t.Run("Sell Pierce", func(t *testing.T) {
		ord := order.NewOrder("test", "7203", order.ACTION_SELL, 2000, 100)
		ord.ToInProgress()

		// Pierce (price > ord.OrderPrice) -> should fill expected
		policy.ApplySyntheticFill(ord, tick.Tick{Price: 2001})
		if !ord.IsFillExpected() {
			t.Error("expected pierce to trigger fill expected")
		}

		// Move away (price <= ord.OrderPrice) -> reset
		policy.ApplySyntheticFill(ord, tick.Tick{Price: 2000})
		if ord.IsFillExpected() {
			t.Error("expected fill expected to reset when price is no longer pierced")
		}
	})

	t.Run("IsOrderDesired", func(t *testing.T) {
		ord := order.NewOrder("test", "7203", order.ACTION_BUY, 2000, 100)
		sig := brain.NewBuyEntry(100, 2000, order.ORDER_TYPE_LIMIT, "")
		sym := symbol.Symbol{Code: "7203"}
		if !policy.IsOrderDesired(ord, sig, sym) {
			t.Error("expected IsOrderDesired to work on StrictPiercePolicy")
		}
	})
}



func TestVolumeConsumptionPolicy_ApplySyntheticFill(t *testing.T) {
	policy := &strategy.VolumeConsumptionPolicy{QueueOffsetRatio: 0.8}
	now := time.Now()

	t.Run("Early returns and invalid prices", func(t *testing.T) {
		ord := order.NewOrder("test", "7203", order.ACTION_BUY, 2000, 100)
		ord.ToCancelSent()
		policy.ApplySyntheticFill(ord, tick.Tick{Price: 1999})
		if ord.IsFillExpected() {
			t.Error("expected cancel sent order to be ignored")
		}

		ord2 := order.NewOrder("test", "7203", order.ACTION_BUY, 0, 100)
		policy.ApplySyntheticFill(ord2, tick.Tick{Price: 2000})
		if ord2.IsFillExpected() {
			t.Error("expected zero price order to be ignored")
		}

		ord3 := order.NewOrder("test", "7203", order.ACTION_BUY, 2000, 100)
		policy.ApplySyntheticFill(ord3, tick.Tick{Price: 0})
		if ord3.IsFillExpected() {
			t.Error("expected zero price tick to be ignored")
		}
	})

	t.Run("Buy Pierce and Touch", func(t *testing.T) {
		ord := order.NewOrder("test", "7203", order.ACTION_BUY, 2000, 100)
		ord.ToInProgress()

		// Pierce Buy -> Instant FillExpected
		policy.ApplySyntheticFill(ord, tick.Tick{Price: 1999, CurrentPriceTime: now})
		if !ord.IsFillExpected() {
			t.Error("expected instant fill expected on pierce")
		}

		// Pierce again when already FillExpected -> should do nothing / remain FillExpected
		policy.ApplySyntheticFill(ord, tick.Tick{Price: 1999, CurrentPriceTime: now})
		if !ord.IsFillExpected() {
			t.Error("expected to remain fill expected")
		}

		// Reset ord
		ord = order.NewOrder("test", "7203", order.ACTION_BUY, 2000, 100)
		ord.ToInProgress()

		// Touch (price == ord.OrderPrice)
		// First touch: records InitialQueueQty, LastVolumeUpdate, sets ConsumedVolume=0
		policy.ApplySyntheticFill(ord, tick.Tick{
			Price:         2000,
			TradingVolume: 10000,
			BestBid:       tick.FirstQuote{Qty: 100},
			BestAsk:       tick.FirstQuote{Qty: 50},
		})
		if ord.IsFillExpected() {
			t.Error("should not be FillExpected on first touch")
		}
		if ord.Synthetic.InitialQueueQty != 100 {
			t.Errorf("expected InitialQueueQty 100, got %f", ord.Synthetic.InitialQueueQty)
		}

		// Volume increase (but under threshold of 100 * 0.8 = 80)
		policy.ApplySyntheticFill(ord, tick.Tick{
			Price:         2000,
			TradingVolume: 10050,
		})
		if ord.IsFillExpected() {
			t.Error("should not be FillExpected under threshold")
		}
		if ord.Synthetic.ConsumedVolume != 50 {
			t.Errorf("expected ConsumedVolume 50, got %f", ord.Synthetic.ConsumedVolume)
		}

		// Volume increase above threshold
		policy.ApplySyntheticFill(ord, tick.Tick{
			Price:            2000,
			TradingVolume:    10100,
			CurrentPriceTime: now,
		})
		if !ord.IsFillExpected() {
			t.Error("expected FillExpected when volume consumption exceeds threshold")
		}

		// Subsequent ticks check elapsed time (under timeout of 2s)
		policy.ApplySyntheticFill(ord, tick.Tick{
			Price:            2000,
			TradingVolume:    10150,
			CurrentPriceTime: now.Add(1 * time.Second),
		})
		if !ord.IsFillExpected() {
			t.Error("should remain FillExpected before timeout")
		}

		// Timeout (elapsed > 2s) with fallback price time to time.Now
		policy.ApplySyntheticFill(ord, tick.Tick{
			Price:            2000,
			TradingVolume:    10200,
			CurrentPriceTime: now.Add(4 * time.Second),
		})
		if !ord.IsWaiting() {
			t.Errorf("expected order to revert to Waiting on timeout, got %v", ord.Status())
		}
	})
	t.Run("Sell Touch and Sell Pierce", func(t *testing.T) {
		ord := order.NewOrder("test", "7203", order.ACTION_SELL, 2000, 100)
		ord.ToInProgress()

		policy.ApplySyntheticFill(ord, tick.Tick{
			Price:         2000,
			TradingVolume: 10000,
			BestBid:       tick.FirstQuote{Qty: 50},
			BestAsk:       tick.FirstQuote{Qty: 100},
		})
		if ord.Synthetic.InitialQueueQty != 100 {
			t.Errorf("expected InitialQueueQty 100, got %f", ord.Synthetic.InitialQueueQty)
		}

		// Sell Pierce -> Instant FillExpected
		ord2 := order.NewOrder("test2", "7203", order.ACTION_SELL, 2000, 100)
		ord2.ToInProgress()
		policy.ApplySyntheticFill(ord2, tick.Tick{Price: 2001, CurrentPriceTime: now})
		if !ord2.IsFillExpected() {
			t.Error("expected instant fill expected on sell pierce")
		}
	})

	t.Run("Volume consumption threshold with zero price time", func(t *testing.T) {
		ord := order.NewOrder("test", "7203", order.ACTION_BUY, 2000, 100)
		ord.ToInProgress()

		policy.ApplySyntheticFill(ord, tick.Tick{
			Price:         2000,
			TradingVolume: 10000,
			BestBid:       tick.FirstQuote{Qty: 100},
		})

		// Volume increase above threshold, but CurrentPriceTime is Zero
		policy.ApplySyntheticFill(ord, tick.Tick{
			Price:         2000,
			TradingVolume: 10100,
		})

		if !ord.IsFillExpected() {
			t.Error("expected fill expected")
		}
		if ord.Synthetic.ExpectedAt.IsZero() {
			t.Error("expected ExpectedAt fallback to time.Now, should not be zero")
		}
	})

	t.Run("Price moves away", func(t *testing.T) {
		ord := order.NewOrder("test", "7203", order.ACTION_BUY, 2000, 100)
		ord.ToInProgress()

		policy.ApplySyntheticFill(ord, tick.Tick{
			Price:         2001,
			TradingVolume: 10000,
		})
		if ord.Synthetic.LastVolumeUpdate != 10000 {
			t.Errorf("expected LastVolumeUpdate 10000, got %f", ord.Synthetic.LastVolumeUpdate)
		}
	})

	t.Run("IsOrderDesired", func(t *testing.T) {
		ord := order.NewOrder("test", "7203", order.ACTION_BUY, 2000, 100)
		sig := brain.NewBuyEntry(100, 2000, order.ORDER_TYPE_LIMIT, "")
		sym := symbol.Symbol{Code: "7203"}
		if !policy.IsOrderDesired(ord, sig, sym) {
			t.Error("expected IsOrderDesired to work on VolumeConsumptionPolicy")
		}
	})
}

func TestNoopPolicy(t *testing.T) {
	policy := &strategy.NoopPolicy{}
	ord := order.NewOrder("test", "7203", order.ACTION_BUY, 2000, 100)
	ord.ToInProgress()

	policy.ApplySyntheticFill(ord, tick.Tick{Price: 1999})
	if ord.IsFillExpected() {
		t.Error("expected NoopPolicy not to apply synthetic fill")
	}

	sig := brain.NewBuyEntry(100, 2000, order.ORDER_TYPE_LIMIT, "")
	sym := symbol.Symbol{Code: "7203"}
	if policy.IsOrderDesired(ord, sig, sym) {
		t.Error("expected NoopPolicy.IsOrderDesired to always return false")
	}
}

func TestIsOrderDesiredDefault(t *testing.T) {
	sym := symbol.Symbol{
		Code:            "7203",
		PriceRangeGroup: symbol.PRICE_RANGE_GROUP_TSE_STANDARD,
	}

	t.Run("Action and Qty mismatch", func(t *testing.T) {
		policy := &strategy.TouchTTLPolicy{}

		// Action mismatch
		ord := order.NewOrder("test", "7203", order.ACTION_BUY, 2000, 100)
		sig := brain.NewSellEntry(100, 2000, order.ORDER_TYPE_LIMIT, "")
		if policy.IsOrderDesired(ord, sig, sym) {
			t.Error("expected false on Action mismatch")
		}

		// Qty mismatch
		sig2 := brain.NewBuyEntry(101, 2000, order.ORDER_TYPE_LIMIT, "")
		if policy.IsOrderDesired(ord, sig2, sym) {
			t.Error("expected false on Qty mismatch")
		}
	})

	t.Run("Buy Limit prices", func(t *testing.T) {
		policy := &strategy.TouchTTLPolicy{}

		ord := order.NewOrder("test", "7203", order.ACTION_BUY, 2000, 100)

		// Case 1: ord.OrderPrice >= sig.Price (e.g. 2000 >= 1999) -> true
		sig := brain.NewBuyEntry(100, 1999, order.ORDER_TYPE_LIMIT, "")
		if !policy.IsOrderDesired(ord, sig, sym) {
			t.Error("expected true when ord.OrderPrice >= sig.Price")
		}

		// Case 2: sig.Price - ord.OrderPrice <= tickSize (e.g. 2001 - 2000 <= 1.0) -> true
		sig2 := brain.NewBuyEntry(100, 2001, order.ORDER_TYPE_LIMIT, "")
		if !policy.IsOrderDesired(ord, sig2, sym) {
			t.Error("expected true when price difference is within 1 tick size")
		}

		// Case 3: sig.Price - ord.OrderPrice > tickSize (e.g. 2005 - 2000 > 1.0) -> false
		sig3 := brain.NewBuyEntry(100, 2005, order.ORDER_TYPE_LIMIT, "")
		if policy.IsOrderDesired(ord, sig3, sym) {
			t.Error("expected false when price difference exceeds 1 tick size")
		}
	})

	t.Run("Sell Limit prices", func(t *testing.T) {
		policy := &strategy.TouchTTLPolicy{}

		ord := order.NewOrder("test", "7203", order.ACTION_SELL, 2000, 100)

		// Case 1: ord.OrderPrice <= sig.Price (e.g. 2000 <= 2001) -> true
		sig := brain.NewSellExit(100, 2001, order.ORDER_TYPE_LIMIT, "")
		if !policy.IsOrderDesired(ord, sig, sym) {
			t.Error("expected true when ord.OrderPrice <= sig.Price")
		}

		// Case 2: ord.OrderPrice - sig.Price <= tickSize (e.g. 2000 - 1999 <= 1.0) -> true
		sig2 := brain.NewSellExit(100, 1999, order.ORDER_TYPE_LIMIT, "")
		if !policy.IsOrderDesired(ord, sig2, sym) {
			t.Error("expected true when price difference is within 1 tick size")
		}

		// Case 3: ord.OrderPrice - sig.Price > tickSize (e.g. 2000 - 1990 > 1.0) -> false
		sig3 := brain.NewSellExit(100, 1990, order.ORDER_TYPE_LIMIT, "")
		if policy.IsOrderDesired(ord, sig3, sym) {
			t.Error("expected false when price difference exceeds 1 tick size")
		}
	})

	t.Run("Market orders (Price=0)", func(t *testing.T) {
		policy := &strategy.TouchTTLPolicy{}

		ord := order.NewOrder("test", "7203", order.ACTION_BUY, 0, 100)
		ord.Type = order.ORDER_TYPE_MARKET

		sig := brain.NewBuyEntry(100, 0, order.ORDER_TYPE_MARKET, "")
		if !policy.IsOrderDesired(ord, sig, sym) {
			t.Error("expected true when both order and signal are market orders")
		}

		sig2 := brain.NewBuyEntry(100, 2000, order.ORDER_TYPE_LIMIT, "")
		if policy.IsOrderDesired(ord, sig2, sym) {
			t.Error("expected false when order is market but signal is limit")
		}
	})
}

