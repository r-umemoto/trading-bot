// internal/domain/sniper/strategy/budget.go
package strategy

import "trading-bot/internal/domain/sniper/brain"

// BudgetConstraint は予算上限を超えないように買いシグナルを制御します
type BudgetConstraint struct {
	baseStrategy Strategy
	maxBudget    float64
}

func NewBudgetConstraint(base Strategy, maxBudget float64) *BudgetConstraint {
	return &BudgetConstraint{
		baseStrategy: base,
		maxBudget:    maxBudget,
	}
}

func (b *BudgetConstraint) Evaluate(input StrategyInput) brain.Signal {
	// 1. まずベースとなる戦略の判断を仰ぐ
	signal := b.baseStrategy.Evaluate(input)

	// 2. 買いシグナル以外（売り、何もしない）ならそのまま通す
	if signal.Action != brain.ActionBuy {
		return signal
	}

	// 3. 予算チェック（現在の総投資額 ＋ 今回買う分の金額）
	estimatedCost := input.CurrentPrice * float64(signal.Quantity)

	if input.TotalExposure+estimatedCost > b.maxBudget {
		// 予算オーバーならシグナルを握りつぶす
		return brain.Signal{Action: brain.ActionHold}
	}

	return signal
}
