package backtest

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
)

type BacktestGateway struct {
	tickCh    chan market.Tick
	execCh    chan market.ExecutionReport
	positions map[string]float64 // 簡易的に持っている数量を管理
	orders    map[string]*market.Order
	mu        sync.Mutex
	orderIdx  int
}

func NewBacktestGateway() *BacktestGateway {
	return &BacktestGateway{
		tickCh:    make(chan market.Tick, 10000),
		execCh:    make(chan market.ExecutionReport, 100),
		positions: make(map[string]float64),
		orders:    make(map[string]*market.Order),
	}
}

func (g *BacktestGateway) Start(ctx context.Context) (<-chan market.Tick, <-chan market.ExecutionReport, error) {
	return g.tickCh, g.execCh, nil
}

// ProcessTick feeds a tick into the gateway. The gateway evaluates existing orders.
func (g *BacktestGateway) ProcessTick(tick market.Tick) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Evaluate orders
	for _, o := range g.orders {
		if o.IsCompleted() {
			continue
		}

		if o.Symbol != tick.Symbol {
			continue
		}

		// Simple execution logic
		executed := false
		if o.OrderPrice == 0 {
			// Market order (assumes Market order passes price 0 or we don't care)
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

			// Send execution report in goroutine to not block mutex
			go func(orderID string, symbol string, action market.Action, price, qty float64) {
				g.execCh <- market.ExecutionReport{
					OrderID:     orderID,
					ExecutionID: execID,
					Symbol:      symbol,
					Action:      action,
					Price:       price,
					Qty:         qty,
				}
			}(o.ID, o.Symbol, o.Action, exec.Price, exec.Qty)
		}
	}

	// Forward tick to usecase
	g.tickCh <- tick
}

func (g *BacktestGateway) SendOrder(ctx context.Context, req market.OrderRequest) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.orderIdx++
	orderID := fmt.Sprintf("bt_order_%d", g.orderIdx)
	order := market.NewOrder(orderID, req.Symbol, req.Action, req.Price, req.Qty)

	g.orders[orderID] = &order

	return orderID, nil
}

func (g *BacktestGateway) CancelOrder(ctx context.Context, orderID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if o, ok := g.orders[orderID]; ok {
		o.IsCanceled = true
		return nil
	}
	return fmt.Errorf("order not found")
}

func (g *BacktestGateway) GetPositions(ctx context.Context, product market.ProductType) ([]market.Position, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	var result []market.Position
	for sym, qty := range g.positions {
		if qty > 0 { // Hold long side
			result = append(result, market.Position{
				Symbol:    sym,
				Action:    market.ACTION_BUY, // Simplified
				LeavesQty: qty,
				Price:     0, // Need accurate calc if needed by cleaner
			})
		} else if qty < 0 {
			result = append(result, market.Position{
				Symbol:    sym,
				Action:    market.ACTION_SELL, // Simplified
				LeavesQty: -qty,
				Price:     0,
			})
		}
	}
	return result, nil
}

func (g *BacktestGateway) GetOrders(ctx context.Context) ([]market.Order, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	var result []market.Order
	for _, o := range g.orders {
		result = append(result, *o)
	}
	return result, nil
}

func (g *BacktestGateway) RegisterSymbol(ctx context.Context, req market.ResisterSymbolRequest) error {
	return nil
}

func (g *BacktestGateway) UnregisterSymbolAll(ctx context.Context) error {
	return nil
}

// Ensure BacktestGateway implements market.MarketGateway
var _ market.MarketGateway = (*BacktestGateway)(nil)
