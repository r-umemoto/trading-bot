// internal/strategy/limit_buy.go
package strategy

import (
	"trading-bot/internal/domain/sniper/brain"
)

type LimitBuy struct {
	TargetPrice float64
	Quantity    float64
}

func NewLimitBuy(targetPrice float64, qty float64) *LimitBuy {
	return &LimitBuy{TargetPrice: targetPrice, Quantity: qty}
}

// engine.Strategy インターフェースを満たす
func (s *LimitBuy) Evaluate(input StrategyInput) brain.Signal {
	if input.CurrentPrice <= s.TargetPrice {
		return brain.Signal{Action: brain.ActionBuy, Quantity: s.Quantity}
	}
	return brain.Signal{Action: brain.ActionHold}
}
