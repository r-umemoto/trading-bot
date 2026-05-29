package backtest

import (
	"context"
	"fmt"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/position"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

type ExecutionModel string

const (
	ExecutionModelPrice  ExecutionModel = "price"  // 価格のみ（指値に届けば約定）
	ExecutionModelVolume ExecutionModel = "volume" // 板の厚みを考慮
	ExecutionModelTouch  ExecutionModel = "touch"  // 旧名称との互換性（Priceと同じ扱い）
)

// SyncBacktestGateway は逐次的に実行されるバックテスト用のゲートウェイです。
type SyncBacktestGateway struct {
	Model   ExecutionModel
	Latency time.Duration

	currentTime time.Time
	orderIdx    int
	orders      map[string]*order.Order
	orderKeys   []string
	orderTypes  map[string]order.OrderType

	tickCh  chan tick.Tick
	orderCh chan order.Orders

	dataPool tick.DataPool

	lastTicks         map[string]tick.Tick
	lastTotalVolumes  map[string]float64
	activeAt          map[string]time.Time // 注文が板に到達する時刻
	cancelActiveAt    map[string]time.Time // キャンセルが板に到達する時刻
	initialDepths     map[string]float64
	cumulativeVolumes map[string]float64
	cancelRequested   map[string]bool

	// 障害注入用のサイレントキャンセル用マップ
	simulateCancelSilent map[string]bool
}

func NewSyncBacktestGateway(model ExecutionModel, latency time.Duration) *SyncBacktestGateway {
	g := &SyncBacktestGateway{
		Model:                model,
		Latency:              latency,
		orders:               make(map[string]*order.Order),
		orderTypes:           make(map[string]order.OrderType),
		tickCh:               make(chan tick.Tick, 1000),
		orderCh:              make(chan order.Orders, 1000),
		lastTicks:            make(map[string]tick.Tick),
		lastTotalVolumes:     make(map[string]float64),
		activeAt:             make(map[string]time.Time),
		cancelActiveAt:       make(map[string]time.Time),
		initialDepths:        make(map[string]float64),
		cumulativeVolumes:    make(map[string]float64),
		cancelRequested:      make(map[string]bool),
		simulateCancelSilent: make(map[string]bool),
	}
	g.dataPool = tick.NewDefaultDataPool(nil)
	return g
}

// InjectCancelSilentFault は指定された注文IDに対してキャンセル成功通知を握りつぶす障害を注入します
func (g *SyncBacktestGateway) InjectCancelSilentFault(orderID string) {
	if g.simulateCancelSilent == nil {
		g.simulateCancelSilent = make(map[string]bool)
	}
	g.simulateCancelSilent[orderID] = true
}

func NewBacktestGateway(model ExecutionModel, latency time.Duration) *SyncBacktestGateway {
	return NewSyncBacktestGateway(model, latency)
}

var _ market.MarketGateway = (*SyncBacktestGateway)(nil)

func (g *SyncBacktestGateway) SetTime(t time.Time) {
	g.currentTime = t
}

func (g *SyncBacktestGateway) TickCh() chan tick.Tick {
	return g.tickCh
}

func (g *SyncBacktestGateway) OrderCh() chan order.Orders {
	return g.orderCh
}

func (g *SyncBacktestGateway) Listen(ctx context.Context) (*market.MarketChannels, error) {
	return &market.MarketChannels{
		Ticks:  make(map[string]<-chan tick.Tick),
		Orders: make(map[string]<-chan order.Orders),
	}, nil
}

func (g *SyncBacktestGateway) DataPool() tick.DataPool {
	return g.dataPool
}

func (g *SyncBacktestGateway) SendOrder(ctx context.Context, input order.SendOrderInput) (*order.Order, error) {
	g.orderIdx++
	orderID := fmt.Sprintf("bt_order_%d", g.orderIdx)

	ord := input.Order
	ord.ID = orderID
	ord.Status = order.ORDER_STATUS_WAITING
	ord.InternalState = order.STATE_ACTIVE
	ord.CreatedAt = g.currentTime

	g.orders[orderID] = ord
	g.orderKeys = append(g.orderKeys, orderID)
	g.orderTypes[orderID] = input.Request.OrderType

	if g.Latency > 0 {
		g.activeAt[orderID] = g.currentTime.Add(g.Latency)
	}

	if g.Model == ExecutionModelVolume && g.Latency == 0 {
		g.initialDepths[orderID] = g.getDepth(ord.Symbol, ord.Action, ord.OrderPrice)
		g.cumulativeVolumes[orderID] = 0
	}

	return ord, nil
}

func (g *SyncBacktestGateway) CancelOrder(ctx context.Context, orderID string) error {
	ord, ok := g.orders[orderID]
	if !ok {
		return fmt.Errorf("order not found: %s", orderID)
	}
	if ord.IsCompleted() {
		return nil
	}
	g.cancelRequested[orderID] = true
	if g.Latency > 0 {
		g.cancelActiveAt[orderID] = g.currentTime.Add(g.Latency)
		ord.Status = order.ORDER_STATUS_CANCEL_SENT
	} else {
		ord.Status = order.ORDER_STATUS_CANCELED
	}
	return nil
}

func (g *SyncBacktestGateway) GetOrders(ctx context.Context) (order.Orders, error) {
	var ords []order.Order
	for _, id := range g.orderKeys {
		ords = append(ords, *g.orders[id])
	}
	return order.Orders{Orders: ords}, nil
}

func (g *SyncBacktestGateway) GetPositions(ctx context.Context, product order.ProductType) ([]position.Position, error) {
	return []position.Position{}, nil
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

func (g *SyncBacktestGateway) GetSymbol(ctx context.Context, symbolCode string, exchange order.ExchangeMarket) (symbol.Symbol, error) {
	return symbol.Symbol{Code: symbolCode}, nil
}

func (g *SyncBacktestGateway) ProcessTick(t tick.Tick) {
	g.currentTime = t.CurrentPriceTime
	volDelta := 0.0
	if lastVol, ok := g.lastTotalVolumes[t.Symbol]; ok {
		volDelta = t.TradingVolume - lastVol
	}
	g.lastTotalVolumes[t.Symbol] = t.TradingVolume
	g.lastTicks[t.Symbol] = t

	if g.dataPool != nil {
		g.dataPool.PushTick(t)
	}

	executed := false
	for _, id := range g.orderKeys {
		ord := g.orders[id]
		if ord.IsCompleted() {
			continue
		}
		if ord.Symbol != t.Symbol {
			continue
		}

		// キャンセル到達チェック
		if g.cancelRequested[id] {
			if cancelTime, ok := g.cancelActiveAt[id]; !ok || !g.currentTime.Before(cancelTime) {
				ord.Status = order.ORDER_STATUS_CANCELED
				if g.simulateCancelSilent != nil && g.simulateCancelSilent[id] {
					// 障害注入: キャンセル成功をサイレント化（イベント通知を遮断）
					continue
				}
				executed = true
				continue
			}
		}

		// 注文到達チェック
		if activeTime, ok := g.activeAt[id]; ok {
			if g.currentTime.Before(activeTime) {
				continue
			}
			if g.Model == ExecutionModelVolume {
				if _, done := g.initialDepths[id]; !done {
					g.initialDepths[id] = g.getDepth(ord.Symbol, ord.Action, ord.OrderPrice)
					g.cumulativeVolumes[id] = 0
				}
			}
		}

		// 約定判定
		askPrice := t.BestAsk.Price
		bidPrice := t.BestBid.Price
		if askPrice == 0 || bidPrice == 0 {
			askPrice = t.Price
			bidPrice = t.Price
		}

		if g.orderTypes[id] == order.ORDER_TYPE_MARKET {
			if ord.Action == order.ACTION_BUY {
				g.executeAll(id, askPrice)
			} else {
				g.executeAll(id, bidPrice)
			}
			executed = true
			continue
		}

		if ord.Action == order.ACTION_BUY {
			if askPrice < ord.OrderPrice {
				g.executeAll(id, askPrice)
				executed = true
			} else if askPrice == ord.OrderPrice {
				if g.tryExecuteVolume(id, volDelta, askPrice) {
					executed = true
				}
			}
		} else {
			if bidPrice > ord.OrderPrice {
				g.executeAll(id, bidPrice)
				executed = true
			} else if bidPrice == ord.OrderPrice {
				if g.tryExecuteVolume(id, volDelta, bidPrice) {
					executed = true
				}
			}
		}
	}

	if executed {
		ords, _ := g.GetOrders(context.Background())
		select {
		case g.orderCh <- ords:
		default:
		}
	}

	select {
	case g.tickCh <- t:
	default:
	}
}

func (g *SyncBacktestGateway) tryExecuteVolume(id string, volDelta float64, price float64) bool {
	if g.Model == ExecutionModelPrice || g.Model == ExecutionModelTouch || g.orderTypes[id] == order.ORDER_TYPE_MARKET {
		g.executeAll(id, price)
		return true
	}

	g.cumulativeVolumes[id] += volDelta
	if g.cumulativeVolumes[id] > g.initialDepths[id] {
		g.executeAll(id, price)
		return true
	}
	return false
}

func (g *SyncBacktestGateway) executeAll(id string, price float64) {
	ord := g.orders[id]
	exec := order.Execution{
		ID:            fmt.Sprintf("exec_%s", id),
		Price:         price,
		Qty:           ord.OrderQty,
		ExecutionTime: g.currentTime,
	}
	ord.AddExecution(exec)
	ord.Status = order.ORDER_STATUS_FILLED

	// 🌟 IFD自動発火ロジック (ゲートウェイ側での自動実行)
	if ord.IfDone != nil {
		fmt.Printf("⚡ [Backtest] IFD発動: 親注文(%s)約定 -> 子注文(%s)を即時発射します\n", ord.ID, ord.IfDone.Action)
		reqType := order.ORDER_TYPE_MARKET
		if ord.IfDone.OrderPrice > 0 {
			reqType = order.ORDER_TYPE_LIMIT
		}
		_, _ = g.SendOrder(context.Background(), order.SendOrderInput{
			Order: ord.IfDone,
			Request: order.OrderRequest{
				OrderType: reqType,
			},
		})
	}
}

func (g *SyncBacktestGateway) getDepth(symbol string, action order.Action, price float64) float64 {
	t, ok := g.lastTicks[symbol]
	if !ok {
		return 0
	}
	board := t.BuyBoard
	if action == order.ACTION_SELL {
		board = t.SellBoard
	}
	for _, q := range board {
		if q.Price == price {
			return q.Qty
		}
	}
	return 0
}
