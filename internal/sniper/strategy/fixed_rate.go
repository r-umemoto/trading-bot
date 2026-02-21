package strategy

import "trading-bot/internal/sniper/brain"

// ---------------------------------------------------
// ② 単体戦略：指定価格以上になったら売る (FixedRateStrategy)
// ---------------------------------------------------
type FixedRateStrategy struct {
	TargetPrice float64
	Quantity    int
}

func NewFixedRate(entryPrice, rate float64, qty int) *FixedRateStrategy {
	return &FixedRateStrategy{
		TargetPrice: entryPrice * (1.0 + rate),
		Quantity:    qty,
	}
}

func (s *FixedRateStrategy) Evaluate(price float64) brain.Signal {
	if price >= s.TargetPrice {
		return brain.Signal{Action: brain.ActionSell, Quantity: s.Quantity}
	}
	return brain.Signal{Action: brain.ActionHold}
}
