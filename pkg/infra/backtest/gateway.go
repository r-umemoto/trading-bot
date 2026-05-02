package backtest

import (
	"context"
	"fmt"
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

	// Evaluate orders (in deterministic order)
	for _, id := range g.orderKeys {
		o := g.orders[id]
		if o.IsCompleted() {
			continue
		}

		if o.Symbol != tick.Symbol {
			continue
		}

		// Execution logic
		executed := false
		execPrice := 0.0
		if o.OrderPrice == 0 {
			// Market order: executes at current tick price
			executed = true
			execPrice = tick.Price
		} else {
			switch g.Model {
			case ExecutionModelTouch:
				// Optimistic (Touch) Rule: executes if price touches or crosses the limit price
				if (o.Action == market.ACTION_BUY && tick.Price <= o.OrderPrice) ||
					(o.Action == market.ACTION_SELL && tick.Price >= o.OrderPrice) {
					executed = true
					execPrice = tick.Price
				}
			case ExecutionModelPessimistic:
				// Trade-Through (貫通約定) Rule: executes only if price moves BEYOND the limit price
				if (o.Action == market.ACTION_BUY && tick.Price < o.OrderPrice) ||
					(o.Action == market.ACTION_SELL && tick.Price > o.OrderPrice) {
					executed = true
					execPrice = o.OrderPrice
				}
			case ExecutionModelVolume:
				// Volume-Based Rule: piercing OR cumulative volume > initial depth
				if (o.Action == market.ACTION_BUY && tick.Price < o.OrderPrice) ||
					(o.Action == market.ACTION_SELL && tick.Price > o.OrderPrice) {
					executed = true
					execPrice = o.OrderPrice
				} else if tick.Price == o.OrderPrice {
					g.cumulativeVolumes[o.ID] += deltaVolume
					if g.cumulativeVolumes[o.ID] > g.initialDepths[o.ID] {
						executed = true
						execPrice = o.OrderPrice
						fmt.Printf("📢 [%s] キュー消化により約定: ID=%s, Price=%.1f, TradedVolume=%.0f, InitialDepth=%.0f\n",
							tick.Symbol, o.ID, o.OrderPrice, g.cumulativeVolumes[o.ID], g.initialDepths[o.ID])
					}
				}
			}
		}

		if executed {
			execID := fmt.Sprintf("exec_%d_%s", time.Now().UnixNano(), o.ID)
			exec := market.Execution{
				ID:    execID,
				Price: execPrice,
				Qty:   o.OrderQty, // Simplified: executes full qty at once
			}
			o.AddExecution(exec)
			o.Status = market.ORDER_STATUS_FILLED // 簡略化：一発で全約定とする

			// Update position
			if o.Action == market.ACTION_BUY {
				g.positions[o.Symbol] += exec.Qty
			} else {
				g.positions[o.Symbol] -= exec.Qty
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

func (g *SyncBacktestGateway) SendOrder(ctx context.Context, req market.OrderRequest) (string, error) {
	g.orderIdx++
	orderID := fmt.Sprintf("bt_order_%d", g.orderIdx)
	order := market.NewOrder(orderID, req.Symbol, req.Action, req.Price, req.Qty)

	g.orders[orderID] = &order
	g.orderKeys = append(g.orderKeys, orderID)

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
