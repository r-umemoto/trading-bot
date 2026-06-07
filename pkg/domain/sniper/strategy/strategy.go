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

// Simulate は、このポジションに対してシグナルが実行されたと仮定した新しいポジションを返します。
func (p Position) Simulate(sig brain.Signal, tickPrice float64) Position {
	if sig.Action == brain.ACTION_HOLD {
		return p
	}
	newQty := p.Qty
	newTotalCost := p.AveragePrice * p.Qty
	execPrice := sig.Price
	if execPrice <= 0 {
		execPrice = tickPrice
	}
	switch sig.Action {
	case brain.ACTION_BUY:
		newQty += sig.Quantity
		newTotalCost += execPrice * sig.Quantity
	case brain.ACTION_SELL:
		newQty -= sig.Quantity
	}
	newAvgPrice := 0.0
	if newQty > 0 {
		newAvgPrice = newTotalCost / newQty
	}
	return Position{Qty: newQty, AveragePrice: newAvgPrice}
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

type TargetPosition struct {
	Qty           float64         // ターゲットポジション量（プラスならロング、マイナスならショート、0ならノーポジ）
	Price         float64         // 注文価格（0なら成行）
	OrderType     order.OrderType // 注文タイプ（指値・成行）
	Reason        string          // 理由（分析用）

	// IFD用の決済ターゲット（オプション）
	HasIfDone     bool
	ExitPrice     float64
	ExitOrderType order.OrderType
	ExitReason    string
}

type Strategy interface {
	Name() string
	Evaluate(input StrategyInput) TargetPosition
	AnalysisLogger() *slog.Logger // 🌟 解析用ロガーを取得
}
