package strategy

import (
	"log/slog"

	"github.com/r-umemoto/trading-bot/pkg/domain/ord"
	"github.com/r-umemoto/trading-bot/pkg/domain/position"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/brain"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

// StrategyOrders は注文の集合に対する操作を提供します
type StrategyOrders []*ord.Order

// Position は特定の時点における銘柄の保有状態を表します
type Position struct {
	Qty          float64 // 保有数量
	AveragePrice float64 // 平均取得単価
}

// StrategyInput は、戦略が判断を下すための「情報のパケット」です
// 内部に計算ロジック（知恵）を隠蔽し、戦略側にはシンプルなインターフェースを提供します。
type StrategyInput struct {
	Orders        StrategyOrders      // すべての注文履歴
	LatestTick    tick.Tick           // 最新のTick
	BasePositions []position.Position // Sniperが管理している確定ポジション

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

// SimulateSignal は、特定のシグナルが約定したと仮定した新しい入力状態を作成します。
// これは IfDone (IFD注文の組み立て) の判定に使用されます。
func (i StrategyInput) SimulateSignal(sig brain.Signal) StrategyInput {
	if sig.Action == brain.ACTION_HOLD {
		return i
	}

	marketAction, _ := sig.Action.ToMarketAction()
	// 疑似注文を作成 (ステータスを FILL_EXPECTED にして約定済みに見せかける)
	simOrder := ord.NewOrderPtr("sim-id", i.LatestTick.Symbol, marketAction, sig.Price, sig.Quantity)
	simOrder.Status = ord.ORDER_STATUS_FILL_EXPECTED

	newOrders := make(StrategyOrders, len(i.Orders)+1)
	copy(newOrders, i.Orders)
	newOrders[len(i.Orders)] = simOrder

	return StrategyInput{
		Orders:        newOrders,
		LatestTick:    i.LatestTick,
		BasePositions: i.BasePositions,
		cachedPos:     nil, // 再計算させる
	}
}

// ActiveOrders は現在板に出ている（未完了の）注文リストを返します
func (i *StrategyInput) ActiveOrders() StrategyOrders {
	var active StrategyOrders
	for _, o := range i.Orders {
		if !o.IsCompleted() && o.Status != ord.ORDER_STATUS_CANCEL_SENT {
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
		if o.Status == ord.ORDER_STATUS_FILL_EXPECTED {
			switch o.Action {
			case ord.ACTION_BUY:
				totalQty += o.OrderQty
				totalCost += o.OrderPrice * o.OrderQty
			case ord.ACTION_SELL:
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
	// IfDone は、直前のシグナルが約定したと仮定した場合の「次の意図」を返します。
	// 不要な場合は ACTION_HOLD を返します。
	IfDone(input StrategyInput, prevSignal brain.Signal) brain.Signal
	AnalysisLogger() *slog.Logger // 🌟 解析用ロガーを取得
}
