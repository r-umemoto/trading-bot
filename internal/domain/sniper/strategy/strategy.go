// internal/domain/sniper/strategy/strategy.go
package strategy

import "trading-bot/internal/domain/sniper/brain"

// StrategyInput は、戦略が判断を下すための「計算済みの相場・口座状態」です
type StrategyInput struct {
	CurrentPrice  float64 // 現在の株価
	HoldQty       float64 // 現在保有している総数量
	AveragePrice  float64 // 平均取得単価
	TotalExposure float64 // 現在の総投資額（平均単価 × 保有数量）

	// 以下、テクニカル判定のために追加
	ShortMA float64 // 短期移動平均線（例: 5分）
	LongMA  float64 // 長期移動平均線（例: 25分）
	VWAP    float64 // 出来高加重平均価格
}

type Strategy interface {
	Evaluate(input StrategyInput) brain.Signal
}
