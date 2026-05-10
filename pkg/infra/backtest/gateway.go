package backtest

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
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
	tickCh    chan market.Tick
	orderCh   chan market.OrdersReport
	positions map[string]float64 // 簡易的に持っている数量を管理
	orders    map[string]*market.Order
	orderKeys []string // 順序を保証するためのキーリスト
	orderIdx  int
	Model     ExecutionModel // 採用する約定モデル

	// ボリュームベース約定用のトラッキング
	lastTradingVolumes map[string]float64
	lastTicks          map[string]market.Tick
	initialDepths      map[string]float64 // orderID -> 並んだ時の板の厚み
	cumulativeVolumes  map[string]float64 // orderID -> その価格での累積出来高
	orderTypes         map[string]market.OrderType
}

func NewBacktestGateway(model ExecutionModel) *SyncBacktestGateway {
	return &SyncBacktestGateway{
		tickCh:             make(chan market.Tick, 10000),
		orderCh:            make(chan market.OrdersReport, 10000), // 大きめのバッファ
		positions:          make(map[string]float64),
		orders:             make(map[string]*market.Order),
		orderKeys:          make([]string, 0),
		Model:              model,
		lastTradingVolumes: make(map[string]float64),
		lastTicks:          make(map[string]market.Tick),
		initialDepths:      make(map[string]float64),
		cumulativeVolumes:  make(map[string]float64),
		orderTypes:         make(map[string]market.OrderType),
	}
}

func (g *SyncBacktestGateway) Start(ctx context.Context) (<-chan market.Tick, <-chan market.OrdersReport, error) {
	return g.tickCh, g.orderCh, nil
}

// ProcessTick feeds a tick into the gateway. The gateway evaluates existing orders.
func (g *SyncBacktestGateway) ProcessTick(tick market.Tick) {
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
				if (o.Action == market.ACTION_BUY && tick.Price <= o.OrderPrice) ||
					(o.Action == market.ACTION_SELL && tick.Price >= o.OrderPrice) {
					executed = true
					execPrice = o.OrderPrice
					execQty = o.OrderQty - o.CumQty
				}
			case ExecutionModelPessimistic, ExecutionModelVolume:
				// Trade-Through (貫通約定) Rule
				isPierced := (o.Action == market.ACTION_BUY && tick.Price < o.OrderPrice) ||
					(o.Action == market.ACTION_SELL && tick.Price > o.OrderPrice)

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
			exec := market.Execution{
				ID:    execID,
				Price: execPrice,
				Qty:   execQty,
			}
			o.AddExecution(exec)
			o.CumQty = o.FilledQty()

			if o.CumQty >= o.OrderQty {
				o.Status = market.ORDER_STATUS_FILLED
			} else {
				o.Status = market.ORDER_STATUS_IN_PROGRESS
			}

			// Update position
			if o.Action == market.ACTION_BUY {
				g.positions[o.Symbol] += execQty
			} else {
				g.positions[o.Symbol] -= execQty
			}

			// 通知用の OrdersReport を生成して送信
			orders, _ := g.GetOrders(context.Background())
			g.orderCh <- market.OrdersReport{
				Orders: orders,
			}
		}
	}

	// Update tracking data
	g.lastTradingVolumes[tick.Symbol] = tick.TradingVolume
	g.lastTicks[tick.Symbol] = tick

	// Forward tick to usecase
	g.tickCh <- tick
}

// walkTheBook は板を順に走査し、指定数量分を約定させた場合の平均価格と数量を返します
func (g *SyncBacktestGateway) walkTheBook(action market.Action, qty float64, limitPrice float64, tick market.Tick) (float64, float64) {
	remainingQty := qty
	totalValue := 0.0
	filledQty := 0.0

	var board []market.Quote
	if action == market.ACTION_BUY {
		board = tick.SellBoard
	} else {
		board = tick.BuyBoard
	}

	// 板が空の場合は最良気配をフォールバックに
	if len(board) == 0 {
		var bestPrice float64
		if action == market.ACTION_BUY {
			bestPrice = tick.BestAsk.Price
		} else {
			bestPrice = tick.BestBid.Price
		}
		if bestPrice > 0 {
			if limitPrice > 0 {
				if action == market.ACTION_BUY && bestPrice > limitPrice {
					return 0, 0
				}
				if action == market.ACTION_SELL && bestPrice < limitPrice {
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
			if action == market.ACTION_BUY && quote.Price > limitPrice {
				break
			}
			if action == market.ACTION_SELL && quote.Price < limitPrice {
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

func (g *SyncBacktestGateway) SendOrder(ctx context.Context, req market.OrderRequest) (string, error) {
	g.orderIdx++
	orderID := fmt.Sprintf("bt_order_%d", g.orderIdx)
	order := market.NewOrderPtr(orderID, req.Symbol, req.Action, req.Price, req.Qty)
	order.HasIFD = req.HasIFD
	order.IFDAction = req.IFDAction
	order.IFDPrice = req.IFDPrice
	order.IFDOrderType = req.IFDOrderType

	g.orders[orderID] = order
	g.orderKeys = append(g.orderKeys, orderID)
	g.orderTypes[orderID] = req.OrderType

	// ボリュームベース約定用の初期情報を記録
	if g.Model == ExecutionModelVolume {
		depth := 0.0
		if lastTick, ok := g.lastTicks[req.Symbol]; ok {
			if req.Action == market.ACTION_BUY {
				for _, q := range lastTick.BuyBoard {
					if q.Price == req.Price {
						depth = q.Qty
						break
					}
				}
			} else {
				for _, q := range lastTick.SellBoard {
					if q.Price == req.Price {
						depth = q.Qty
						break
					}
				}
			}
		}
		g.initialDepths[orderID] = depth
		g.cumulativeVolumes[orderID] = 0
	}

	return orderID, nil
}

func (g *SyncBacktestGateway) CancelOrder(ctx context.Context, orderID string) error {
	if o, ok := g.orders[orderID]; ok {
		o.Status = market.ORDER_STATUS_CANCELED
		// キャンセルを通知
		orders, _ := g.GetOrders(ctx)
		g.orderCh <- market.OrdersReport{
			Orders: orders,
		}
		return nil
	}
	return fmt.Errorf("order not found")
}

func (g *SyncBacktestGateway) GetPositions(ctx context.Context, product market.ProductType) ([]market.Position, error) {
	var result []market.Position
	for sym, qty := range g.positions {
		if qty > 0 { // Hold long side
			result = append(result, market.Position{
				Symbol:    sym,
				Action:    market.ACTION_BUY,
				LeavesQty: qty,
				Price:     0,
			})
		} else if qty < 0 {
			result = append(result, market.Position{
				Symbol:    sym,
				Action:    market.ACTION_SELL,
				LeavesQty: -qty,
				Price:     0,
			})
		}
	}
	return result, nil
}

func (g *SyncBacktestGateway) GetOrders(ctx context.Context) ([]market.Order, error) {
	var result []market.Order
	for _, id := range g.orderKeys {
		result = append(result, *g.orders[id])
	}
	return result, nil
}

func (g *SyncBacktestGateway) GetSymbol(ctx context.Context, symbol string, exchange market.ExchangeMarket) (market.Symbol, error) {
	return market.Symbol{
		Code:            symbol,
		Name:            "Mock Symbol",
		PriceRangeGroup: market.PRICE_RANGE_GROUP_TSE_STANDARD,
	}, nil
}

func (g *SyncBacktestGateway) RegisterSymbol(ctx context.Context, req market.ResisterSymbolRequest) error {
	return nil
}

func (g *SyncBacktestGateway) UnregisterSymbolAll(ctx context.Context) error {
	return nil
}

// Ensure SyncBacktestGateway implements market.MarketGateway
var _ market.MarketGateway = (*SyncBacktestGateway)(nil)
