// internal/strategy/limit_buy.go
package strategy

import (
	"trading-bot/internal/domain/sniper/brain"
)

type LimitBuy struct {
	TargetPrice float64
	Quantity    int
}

func NewLimitBuy(targetPrice float64, qty int) *LimitBuy {
	return &LimitBuy{TargetPrice: targetPrice, Quantity: qty}
}

// engine.Strategy インターフェースを満たす
func (s *LimitBuy) Evaluate(price float64) brain.Signal {
	if price <= s.TargetPrice {
		return brain.Signal{Action: brain.ActionBuy, Quantity: s.Quantity}
	}
	return brain.Signal{Action: brain.ActionHold}
}
