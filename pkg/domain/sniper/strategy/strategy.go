package strategy

import (
	"log/slog"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/brain"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

// Position は特定の時点における銘柄の保有状態を表す要約です
type Position struct {
	Qty          float64 // 保有数量
	AveragePrice float64 // 平均取得単価
}

// StrategyInput は、戦略が判断を下すための「情報のパケット」です
// 内部に計算ロジック（知恵）を隠蔽し、戦略側にはシンプルなインターフェースを提供します。
type StrategyInput struct {
	Position   Position  // 現在のポジション要約
	LatestTick tick.Tick // 最新 of Tick
}

// --- 戦略から利用する「道具箱」メソッド ---

// HoldQty は現在の保有数量を返します
func (i *StrategyInput) HoldQty() float64 {
	return i.Position.Qty
}

// AveragePrice は現在の平均取得単価を返します
func (i *StrategyInput) AveragePrice() float64 {
	return i.Position.AveragePrice
}

type Strategy interface {
	Name() string
	Evaluate(input StrategyInput) brain.Signal
	// IfDone は、直前のシグナルが約定したと仮定した場合の「次の意図」を返します。
	// 不要な場合は ACTION_HOLD を返します。
	IfDone(input StrategyInput, prevSignal brain.Signal) brain.Signal
	AnalysisLogger() *slog.Logger // 🌟 解析用ロガーを取得
	// ShouldCancel は、現在アクティブな注文（未約定）をキャンセルすべきか戦略自身が判断します。
	ShouldCancel(input StrategyInput, ord *order.Order) bool
}
