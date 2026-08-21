package sniper

import (
	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
)

// ApplyPolicyHelper helps tests evaluate policies and apply synthetic fill state transitions.
func ApplyPolicyHelper(policy strategy.ExecutionPolicy, orders []*order.Order, t tick.Tick) {
	for _, curr := range orders {
		if !curr.IsEligibleForSyntheticFill() {
			continue
		}
		policy.UpdateState(curr, t)
		if policy.ShouldReset(curr, t) {
			curr.ResetSynthetic()
			continue
		}
		if curr.IsFillExpected() {
			if policy.ShouldTimeout(curr, t) {
				curr.MarkAsTimedOut()
			}
			continue
		}
		if policy.ShouldFill(curr, t) {
			curr.MarkAsFillExpected(t.CurrentPriceTime)
		}
	}
}
