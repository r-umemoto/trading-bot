package backtest

import (
	"context"
	"fmt"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
)

// SyncBacktestGateway は各Tickの処理において「約定を完全に同期して」発行するためのテスト用Gatewayです
// 非同期goroutineによるレースコンディションを防ぎ、バックテスト結果を完全に決定論的にします
type SyncBacktestGateway struct {
	tickCh    chan market.Tick
	execCh    chan market.ExecutionReport
	positions map[string]float64 // 簡易的に持っている数量を管理
	orders    map[string]*market.Order
	orderKeys []string // 順序を保証するためのキーリスト
	orderIdx  int
}

func NewBacktestGateway() *SyncBacktestGateway {
	return &SyncBacktestGateway{
		tickCh:    make(chan market.Tick, 10000),
		execCh:    make(chan market.ExecutionReport, 10000), // 大きめのバッファ
		positions: make(map[string]float64),
		orders:    make(map[string]*market.Order),
		orderKeys: make([]string, 0),
	}
}

func (g *SyncBacktestGateway) Start(ctx context.Context) (<-chan market.Tick, <-chan market.ExecutionReport, error) {
	return g.tickCh, g.execCh, nil
}

// ProcessTick feeds a tick into the gateway. The gateway evaluates existing orders.
func (g *SyncBacktestGateway) ProcessTick(tick market.Tick) {
	// Evaluate orders (in deterministic order)
	for _, id := range g.orderKeys {
		o := g.orders[id]
		if o.IsCompleted() {
			continue
		}

		if o.Symbol != tick.Symbol {
			continue
		}

		// Simple execution logic
		executed := false
		if o.OrderPrice == 0 {
			// Market order
			executed = true
		} else {
			// Limit order
			if o.Action == market.ACTION_BUY && tick.Price <= o.OrderPrice {
				executed = true
			} else if o.Action == market.ACTION_SELL && tick.Price >= o.OrderPrice {
				executed = true
			}
		}

		if executed {
			execID := fmt.Sprintf("exec_%d_%s", time.Now().UnixNano(), o.ID)
			exec := market.Execution{
				ID:    execID,
				Price: tick.Price, // Executes at current tick price
				Qty:   o.OrderQty, // Simplified: executes full qty at once
			}
			o.AddExecution(exec)

			// Update position
			if o.Action == market.ACTION_BUY {
				g.positions[o.Symbol] += exec.Qty
			} else {
				g.positions[o.Symbol] -= exec.Qty
			}

			// ★ここがオリジナルと違う：別ゴルーチンではなく完全に同期してチャネルに入れる
			g.execCh <- market.ExecutionReport{
				OrderID:     o.ID,
				ExecutionID: execID,
				Symbol:      o.Symbol,
				Action:      o.Action,
				Price:       exec.Price,
				Qty:         exec.Qty,
			}
		}
	}

	// Forward tick to usecase
	g.tickCh <- tick
}

func (g *SyncBacktestGateway) SendOrder(ctx context.Context, req market.OrderRequest) (string, error) {
	g.orderIdx++
	orderID := fmt.Sprintf("bt_order_%d", g.orderIdx)
	order := market.NewOrder(orderID, req.Symbol, req.Action, req.Price, req.Qty)

	g.orders[orderID] = &order
	g.orderKeys = append(g.orderKeys, orderID)

	return orderID, nil
}

func (g *SyncBacktestGateway) CancelOrder(ctx context.Context, orderID string) error {
	if o, ok := g.orders[orderID]; ok {
		o.IsCanceled = true
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

func (g *SyncBacktestGateway) RegisterSymbol(ctx context.Context, req market.ResisterSymbolRequest) error {
	return nil
}

func (g *SyncBacktestGateway) UnregisterSymbolAll(ctx context.Context) error {
	return nil
}

// Ensure SyncBacktestGateway implements market.MarketGateway
var _ market.MarketGateway = (*SyncBacktestGateway)(nil)
