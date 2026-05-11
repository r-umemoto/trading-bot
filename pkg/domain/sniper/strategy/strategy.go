package strategy

import (
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

// ExtractPosition は注文履歴から現在のポジションの状態を計算します
// APIからの確定約定に加え、ボットが独自に判断した疑似約定(FILL_EXPECTED)も先行計上します。
func (os StrategyOrders) ExtractPosition(initialPositions []market.Position) Position {
	var totalQty float64
	var totalCost float64

	// 1. APIから取得・同期済みの確定ポジションをベースにする
	for _, p := range initialPositions {
		totalQty += p.LeavesQty
		totalCost += p.Price * float64(p.LeavesQty)
	}

	// 2. まだ API に反映されていない可能性のある「疑似約定(FILL_EXPECTED)」を先行計上する
	// ※ 確定約定分は s.positions に反映済みなので、ここでは FILL_EXPECTED のみを対象とする
	for _, o := range os {
		if o.Status == market.ORDER_STATUS_FILL_EXPECTED {
			if o.Action == market.ACTION_BUY {
				totalQty += o.OrderQty
				totalCost += o.OrderPrice * o.OrderQty
			} else if o.Action == market.ACTION_SELL {
				totalQty -= o.OrderQty
				// 決済（売り）の場合はコスト（平均単価）は維持
			}
		}
	}

	avgPrice := 0.0
	if totalQty > 0 {
		avgPrice = totalCost / totalQty
	}

	return Position{
		Qty:          totalQty,
		AveragePrice: avgPrice,
	}
}

// FilterActive は現在板に出ている（未完了の）注文だけを抽出します
func (os StrategyOrders) FilterActive() StrategyOrders {
	var active StrategyOrders
	for _, o := range os {
		if !o.IsCompleted() && o.Status != market.ORDER_STATUS_CANCEL_SENT {
			active = append(active, o)
		}
	}
	return active
}

// StrategyInput は、戦略が判断を下すための「情報のパケット」です
type StrategyInput struct {
	Orders     StrategyOrders // すべての注文履歴
	LatestTick market.Tick    // 最新のTick
	
	// Sniper が管理している確定ポジション（計算の起点として渡す）
	BasePositions []market.Position
}

type Strategy interface {
	Name() string
	Evaluate(input StrategyInput) brain.Signal
}
