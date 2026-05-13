package strategy

import (
	"log/slog"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/brain"
)

// StrategyOrders は注文の集合に対する操作を提供します
type StrategyOrders []*market.Order

// Position は特定の時点における銘柄の保有状態を表します
type Position struct {
	Qty          float64 // 保有数量
	AveragePrice float64 // 平均取得単価
}

// StrategyInput は、戦略が判断を下すための「情報のパケット」です
// 内部に計算ロジック（知恵）を隠蔽し、戦略側にはシンプルなインターフェースを提供します。
type StrategyInput struct {
	Orders        StrategyOrders    // すべての注文履歴
	LatestTick    market.Tick       // 最新のTick
	BasePositions []market.Position // Sniperが管理している確定ポジション

	// 内部キャッシュ（複数回メソッドを呼んでも計算は一度だけ）
	cachedPos *Position
}

// --- 戦略から利用する「道具箱」メソッド ---

// HoldQty は現在の保有数量を返します
func (i *StrategyInput) HoldQty() float64 {
	return i.ensurePosition().Qty
}

// AveragePrice は現在の平均取得単価を返します
func (i *StrategyInput) AveragePrice() float64 {
	return i.ensurePosition().AveragePrice
}

// ActiveOrders は現在板に出ている（未完了の）注文リストを返します
func (i *StrategyInput) ActiveOrders() StrategyOrders {
	var active StrategyOrders
	for _, o := range i.Orders {
		if !o.IsCompleted() && o.Status != market.ORDER_STATUS_CANCEL_SENT {
			active = append(active, o)
		}
	}
	return active
}

// --- 内部計算ロジック（戦略からは隠蔽） ---

func (i *StrategyInput) ensurePosition() *Position {
	if i.cachedPos != nil {
		return i.cachedPos
	}

	var totalQty float64
	var totalCost float64

	// 1. API確定ポジションをベースにする
	for _, p := range i.BasePositions {
		totalQty += p.LeavesQty
		totalCost += p.Price * float64(p.LeavesQty)
	}

	// 2. 疑似約定(FILL_EXPECTED)を先行計上
	for _, o := range i.Orders {
		if o.Status == market.ORDER_STATUS_FILL_EXPECTED {
			if o.Action == market.ACTION_BUY {
				totalQty += o.OrderQty
				totalCost += o.OrderPrice * o.OrderQty
			} else if o.Action == market.ACTION_SELL {
				totalQty -= o.OrderQty
			}
		}
	}

	avgPrice := 0.0
	if totalQty > 0 {
		avgPrice = totalCost / totalQty
	}

	i.cachedPos = &Position{
		Qty:          totalQty,
		AveragePrice: avgPrice,
	}
	return i.cachedPos
}

type Strategy interface {
	Name() string
	Evaluate(input StrategyInput) brain.Signal
	AnalysisLogger() *slog.Logger // 🌟 解析用ロガーを取得
}
