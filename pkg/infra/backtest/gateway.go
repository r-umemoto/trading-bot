package backtest

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/ord"
	"github.com/r-umemoto/trading-bot/pkg/domain/position"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

type ExecutionModel string

const (
	ExecutionModelTouch       ExecutionModel = "touch"
	ExecutionModelPessimistic ExecutionModel = "pessimistic"
	ExecutionModelVolume      ExecutionModel = "volume"
)

// SyncBacktestGateway は各Tickの処理において「約定を完全に同期して」発行するためのテスト用Gatewayです
// 非同期goroutineによるレースコンディションを防ぎ、バックテスト結果を完全に決定論的にします
type SyncBacktestGateway struct {
	tickCh    chan tick.Tick
	orderCh   chan ord.Orders
	positions map[string]float64 // 簡易的に持っている数量を管理
	orders    map[string]*ord.Order
	orderKeys []string // 順序を保証するためのキーリスト
	orderIdx  int
	Model     ExecutionModel // 採用する約定モデル

	// ボリュームベース約定用のトラッキング
	lastTradingVolumes map[string]float64
	lastTicks          map[string]tick.Tick
	initialDepths      map[string]float64 // orderID -> 並んだ時の板の厚み
	cumulativeVolumes  map[string]float64 // orderID -> その価格での累積出来高
	orderTypes         map[string]ord.OrderType
}

func NewBacktestGateway(model ExecutionModel) *SyncBacktestGateway {
	return &SyncBacktestGateway{
		tickCh:             make(chan tick.Tick, 10000),
		orderCh:            make(chan ord.Orders, 10000), // 大きめのバッファ
		positions:          make(map[string]float64),
		orders:             make(map[string]*ord.Order),
		orderKeys:          make([]string, 0),
		Model:              model,
		lastTradingVolumes: make(map[string]float64),
		lastTicks:          make(map[string]tick.Tick),
		initialDepths:      make(map[string]float64),
		cumulativeVolumes:  make(map[string]float64),
		orderTypes:         make(map[string]ord.OrderType),
	}
}

func (g *SyncBacktestGateway) Start(ctx context.Context) (<-chan tick.Tick, <-chan ord.Orders, error) {
	return g.tickCh, g.orderCh, nil
}

// ProcessTick feeds a tick into the gateway. The gateway evaluates existing orders.
func (g *SyncBacktestGateway) ProcessTick(tick tick.Tick) {
	// Delta volume calculation for volume-based logic
	deltaVolume := 0.0
	if lastVol, ok := g.lastTradingVolumes[tick.Symbol]; ok {
		deltaVolume = tick.TradingVolume - lastVol
	}
	g.lastTradingVolumes[tick.Symbol] = tick.TradingVolume

	// Evaluate orders (in deterministic order)
	for _, id := range g.orderKeys {
		o := g.orders[id]
		if o.IsCompleted() {
			continue
		}
		if o.Symbol != tick.Symbol {
			continue
		}

		executed := false
		execPrice := 0.0
		execQty := 0.0

		if o.OrderPrice == 0 {
			// Market order: walk the book
			avgPrice, filled := g.walkTheBook(o.Action, o.OrderQty-o.CumQty, 0, tick)
			if filled > 0 {
				executed = true
				execPrice = avgPrice
				execQty = filled
			}
		} else {
			switch g.Model {
			case ExecutionModelTouch:
				// Optimistic (Touch) Rule: executes if price touches or crosses the limit price
				if (o.Action == ord.ACTION_BUY && tick.Price <= o.OrderPrice) ||
					(o.Action == ord.ACTION_SELL && tick.Price >= o.OrderPrice) {
					executed = true
					execPrice = o.OrderPrice
					execQty = o.OrderQty - o.CumQty
				}
			case ExecutionModelPessimistic, ExecutionModelVolume:
				// Trade-Through (貫通約定) Rule
				isPierced := (o.Action == ord.ACTION_BUY && tick.Price < o.OrderPrice) ||
					(o.Action == ord.ACTION_SELL && tick.Price > o.OrderPrice)

				if isPierced {
					// 貫通時は板を食い破る（Marketable Limitの再現）
					avgPrice, filled := g.walkTheBook(o.Action, o.OrderQty-o.CumQty, o.OrderPrice, tick)
					if filled > 0 {
						executed = true
						execPrice = avgPrice
						execQty = filled
					}
				} else if g.Model == ExecutionModelVolume && tick.Price == o.OrderPrice {
					// 同値での出来高消化待ち
					g.cumulativeVolumes[o.ID] += deltaVolume
					if g.cumulativeVolumes[o.ID] > g.initialDepths[o.ID] {
						executed = true
						execPrice = o.OrderPrice
						execQty = o.OrderQty - o.CumQty // 同値での消化は全量とみなす
					}
				}
			}
		}

		if executed && execQty > 0 {
			execID := fmt.Sprintf("exec_%d_%s", time.Now().UnixNano(), o.ID)
			exec := ord.Execution{
				ID:            execID,
				Price:         execPrice,
				Qty:           execQty,
				ExecutionTime: tick.CurrentPriceTime,
			}
			o.AddExecution(exec)
			o.CumQty = o.FilledQty()

			if o.CumQty >= o.OrderQty {
				o.Status = ord.ORDER_STATUS_FILLED
			} else {
				o.Status = ord.ORDER_STATUS_IN_PROGRESS
			}

			// Update position
			if o.Action == ord.ACTION_BUY {
				g.positions[o.Symbol] += execQty
			} else {
				g.positions[o.Symbol] -= execQty
			}

			// 通知用の OrdersReport を生成して送信
			orders, _ := g.GetOrders(context.Background())
			g.orderCh <- orders
		}
	}

	// Update tracking data
	g.lastTradingVolumes[tick.Symbol] = tick.TradingVolume
	g.lastTicks[tick.Symbol] = tick

	// Forward tick to usecase
	g.tickCh <- tick
}

// walkTheBook は板を順に走査し、指定数量分を約定させた場合の平均価格と数量を返します
func (g *SyncBacktestGateway) walkTheBook(action ord.Action, qty float64, limitPrice float64, t tick.Tick) (float64, float64) {
	remainingQty := qty
	totalValue := 0.0
	filledQty := 0.0

	var board []tick.Quote
	if action == ord.ACTION_BUY {
		board = t.SellBoard
	} else {
		board = t.BuyBoard
	}

	// 板が空の場合は最良気配をフォールバックに
	if len(board) == 0 {
		var bestPrice float64
		if action == ord.ACTION_BUY {
			bestPrice = t.BestAsk.Price
		} else {
			bestPrice = t.BestBid.Price
		}
		if bestPrice > 0 {
			if limitPrice > 0 {
				if action == ord.ACTION_BUY && bestPrice > limitPrice {
					return 0, 0
				}
				if action == ord.ACTION_SELL && bestPrice < limitPrice {
					return 0, 0
				}
			}
			return bestPrice, qty
		}
		return 0, 0
	}

	for _, quote := range board {
		if remainingQty <= 0 {
			break
		}

		// 指値チェック
		if limitPrice > 0 {
			if action == ord.ACTION_BUY && quote.Price > limitPrice {
				break
			}
			if action == ord.ACTION_SELL && quote.Price < limitPrice {
				break
			}
		}

		tradeQty := math.Min(remainingQty, quote.Qty)
		if tradeQty <= 0 {
			continue
		}

		totalValue += tradeQty * quote.Price
		filledQty += tradeQty
		remainingQty -= tradeQty
	}

	if filledQty == 0 {
		return 0, 0
	}
	return totalValue / filledQty, filledQty
}

func (g *SyncBacktestGateway) SendOrder(ctx context.Context, order ord.Order) (ord.Order, error) {
	g.orderIdx++
	orderID := fmt.Sprintf("bt_order_%d", g.orderIdx)

	order.ID = orderID
	order.Status = ord.ORDER_STATUS_WAITING
	order.InternalState = ord.STATE_ACTIVE // API送信成功・受付完了としてACTIVEへ遷移

	storedOrder := order // 🌟 ポインタ共有を避けるためコピーを保存
	g.orders[orderID] = &storedOrder
	g.orderKeys = append(g.orderKeys, orderID)
	g.orderTypes[orderID] = order.OrderType

	// ボリュームベース約定用の初期情報を記録
	if g.Model == ExecutionModelVolume {
		depth := 0.0
		if lastTick, ok := g.lastTicks[order.Symbol]; ok {
			if order.Action == ord.ACTION_BUY {
				for _, q := range lastTick.BuyBoard {
					if q.Price == order.OrderPrice {
						depth = q.Qty
						break
					}
				}
			} else {
				for _, q := range lastTick.SellBoard {
					if q.Price == order.OrderPrice {
						depth = q.Qty
						break
					}
				}
			}
		}
		g.initialDepths[orderID] = depth
		g.cumulativeVolumes[orderID] = 0
	}

	return order, nil
}

func (g *SyncBacktestGateway) CancelOrder(ctx context.Context, orderID string) error {
	if o, ok := g.orders[orderID]; ok {
		o.Status = ord.ORDER_STATUS_CANCELED
		// キャンセルを通知
		orders, _ := g.GetOrders(ctx)
		g.orderCh <- orders
		return nil
	}
	return fmt.Errorf("order not found")
}

func (g *SyncBacktestGateway) GetPositions(ctx context.Context, product ord.ProductType) ([]position.Position, error) {
	var result []position.Position
	for sym, qty := range g.positions {
		if qty > 0 { // Hold long side
			result = append(result, position.Position{
				Symbol:    sym,
				Action:    ord.ACTION_BUY,
				LeavesQty: qty,
				Price:     0,
			})
		} else if qty < 0 {
			result = append(result, position.Position{
				Symbol:    sym,
				Action:    ord.ACTION_SELL,
				LeavesQty: -qty,
				Price:     0,
			})
		}
	}
	return result, nil
}

func (g *SyncBacktestGateway) GetOrders(ctx context.Context) (ord.Orders, error) {
	var result []ord.Order
	for _, id := range g.orderKeys {
		result = append(result, *g.orders[id])
	}
	return ord.Orders{Orders: result}, nil
}

func (g *SyncBacktestGateway) GetSymbol(ctx context.Context, symbolCode string, exchange ord.ExchangeMarket) (symbol.Symbol, error) {
	return symbol.Symbol{
		Code:            symbolCode,
		Name:            "Mock Symbol",
		PriceRangeGroup: symbol.PRICE_RANGE_GROUP_TSE_STANDARD,
	}, nil
}

func (g *SyncBacktestGateway) RegisterSymbol(ctx context.Context, req market.ResisterSymbolRequest) error {
	return nil
}

func (g *SyncBacktestGateway) RegisterSymbols(ctx context.Context, reqs []market.ResisterSymbolRequest) error {
	return nil
}

func (g *SyncBacktestGateway) UnregisterSymbolAll(ctx context.Context) error {
	return nil
}

// Ensure SyncBacktestGateway implements market.MarketGateway
var _ market.MarketGateway = (*SyncBacktestGateway)(nil)
