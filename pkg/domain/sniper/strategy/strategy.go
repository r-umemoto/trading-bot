// internal/domain/sniper/strategy/strategy.go
package strategy

import (
	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/brain"
)

// StrategyInput は、戦略が判断を下すための「計算済みの相場・口座状態」です
type StrategyInput struct {
	CurrentPrice  float64 // 現在の株価
	HoldQty       float64 // 現在保有している総数量（売却可能数量）
	AveragePrice  float64 // 平均取得単価
	TotalExposure float64 // 現在の総投資額（平均単価 × 保有数量）

	// 以下、テクニカル判定のために追加
	// 以下、生データ
	LatestTick     market.Tick // 最新のTick（現在価格、出来高等を含む）
	Recent10Prices []float64

	DataPool market.DataPool // データプールへの参照（他の銘柄データや履歴にアクセスするため）
}

type Strategy interface {
	Evaluate(input StrategyInput) brain.Signal
}
