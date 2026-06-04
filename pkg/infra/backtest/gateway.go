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

	// 建玉管理
	positions map[string][]position.Position
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
		positions:            make(map[string][]position.Position),
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
	ord := input.Order

	// 信用返済または決済順序が指定されている場合は返済注文と判定
	isExit := ord.CashMargin == order.CASH_MARGIN_MARGIN_EXIT ||
		(ord.Request != nil && (ord.Request.ClosePositionOrder != order.CLOSE_POSITION_ORDER_NONE ||
		len(ord.Request.ClosePositions) > 0))

	if isExit {
		// 返済注文の場合：口座に反対の建玉が存在するか検証
		targetAction := order.ACTION_BUY
		if ord.Action == order.ACTION_BUY {
			targetAction = order.ACTION_SELL
		}

		var availableQty float64
		for _, p := range g.positions[ord.Symbol] {
			if p.Action == targetAction {
				availableQty += p.LeavesQty
			}
		}

		if availableQty < ord.OrderQty {
			// 建玉不足エラー
			return nil, fmt.Errorf("カブコムAPI発注失敗: 発注失敗: APIエラー (Status: 400): {\"Code\":1009001,\"Message\":\"建玉が選択されていません。\"}")
		}
	} else {
		// 新規注文の場合：デイトレ両建て規制をシミュレート
		// すでに反対の建玉を保有している場合、新規で両建てしようとするとエラーを返す
		targetAction := order.ACTION_BUY
		if ord.Action == order.ACTION_BUY {
			targetAction = order.ACTION_SELL
		}

		var oppositeQty float64
		for _, p := range g.positions[ord.Symbol] {
			if p.Action == targetAction {
				oppositeQty += p.LeavesQty
			}
		}

		if oppositeQty > 0 {
			// 両建て規制エラー
			return nil, fmt.Errorf("カブコムAPI発注失敗: 発注失敗: APIエラー (Status: 400): {\"Code\":1009001,\"Message\":\"建玉が選択されていません。\"}")
		}
	}

	g.orderIdx++
	orderID := fmt.Sprintf("bt_order_%d", g.orderIdx)

	ord.ID = orderID
	ord.ToWaiting()
	ord.ToPending()
	ord.ToActive()
	ord.CreatedAt = g.currentTime

	g.orders[orderID] = ord
	g.orderKeys = append(g.orderKeys, orderID)
	g.orderTypes[orderID] = ord.Type

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
		ord.BypassTransition(order.ORDER_STATUS_CANCEL_SENT, order.STATE_CANCELING)
	} else {
		ord.BypassTransition(order.ORDER_STATUS_CANCELED, order.STATE_CLOSED)
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
	var allPos []position.Position
	for _, posList := range g.positions {
		for _, p := range posList {
			if p.LeavesQty > 0 {
				allPos = append(allPos, p)
			}
		}
	}
	return allPos, nil
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
				ord.BypassTransition(order.ORDER_STATUS_CANCELED, order.STATE_CLOSED)
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
	ord.BypassTransition(order.ORDER_STATUS_FILLED, order.STATE_CLOSED)

	// --- ポジション管理の更新 ---
	isExit := ord.CashMargin == order.CASH_MARGIN_MARGIN_EXIT ||
		(ord.Request != nil && (ord.Request.ClosePositionOrder != order.CLOSE_POSITION_ORDER_NONE ||
		len(ord.Request.ClosePositions) > 0))

	if isExit {
		// 返済処理：建玉を減らす
		targetAction := order.ACTION_BUY
		if ord.Action == order.ACTION_BUY {
			targetAction = order.ACTION_SELL
		}

		remainingToClose := ord.OrderQty
		var updatedPositions []position.Position
		for _, p := range g.positions[ord.Symbol] {
			if p.Action == targetAction && remainingToClose > 0 {
				if p.LeavesQty > remainingToClose {
					p.LeavesQty -= remainingToClose
					remainingToClose = 0
					updatedPositions = append(updatedPositions, p)
				} else {
					remainingToClose -= p.LeavesQty
				}
			} else {
				updatedPositions = append(updatedPositions, p)
			}
		}
		g.positions[ord.Symbol] = updatedPositions
	} else {
		// 新規建て：建玉を増やす
		if g.positions == nil {
			g.positions = make(map[string][]position.Position)
		}
		newPos := position.Position{
			ExecutionID: fmt.Sprintf("pos_%s", id),
			Symbol:      ord.Symbol,
			Exchange:    order.EXCHANGE_TOSHO,
			Action:      ord.Action,
			TradeType:   order.TRADE_TYPE_GENERAL_DAY,
			AccountType: order.ACCOUNT_SPECIAL,
			LeavesQty:   ord.OrderQty,
			Price:       price,
		}
		g.positions[ord.Symbol] = append(g.positions[ord.Symbol], newPos)
	}

	// 🌟 IFD自動発火ロジック (ゲートウェイ側での自動実行)
	if ord.IfDone != nil {
		fmt.Printf("⚡ [Backtest] IFD発動: 親注文(%s)約定 -> 子注文(%s)を即時発射します\n", ord.ID, ord.IfDone.Action)
		_, _ = g.SendOrder(context.Background(), order.SendOrderInput{
			Order: ord.IfDone,
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
