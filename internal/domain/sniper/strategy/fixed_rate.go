package strategy

import "trading-bot/internal/domain/sniper/brain"

// ---------------------------------------------------
// ② 単体戦略：指定価格以上になったら売る (FixedRateStrategy)
// ---------------------------------------------------
type FixedRateStrategy struct {
	TargetPrice float64
	Quantity    float64
}

func NewFixedRate(entryPrice, rate float64, qty float64) *FixedRateStrategy {
	return &FixedRateStrategy{
		TargetPrice: entryPrice * (1.0 + rate),
		Quantity:    qty,
	}
}

func (s *FixedRateStrategy) Evaluate(input StrategyInput) brain.Signal {
	if input.CurrentPrice >= s.TargetPrice {
		return brain.Signal{Action: brain.ActionSell, Quantity: s.Quantity}
	}
	return brain.Signal{Action: brain.ActionHold}
}
