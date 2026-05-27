package sniper

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/position"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/brain"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
)

type Strategy interface {
	Name() string
	Evaluate(input strategy.StrategyInput) brain.Signal
	IfDone(input strategy.StrategyInput, prevSignal brain.Signal) brain.Signal
	AnalysisLogger() *slog.Logger
}

type PerformanceProvider interface {
	GetPerformance(sniperID string) Performance
	GetUnrealizedPnL(sniperID string, currentPrice float64) float64
}

type LifecycleState int

const (
	LifecycleActive LifecycleState = iota
	LifecycleExiting
	LifecycleStopped
)

type Bullet struct {
	Order         *order.Order
	Request       *order.OrderRequest
	CancelOrderID string
}

func (b Bullet) HasOrder() bool {
	return b.Order != nil && b.Request != nil
}

func (b Bullet) HasCancel() bool {
	return b.CancelOrderID != ""
}

type Performance struct {
	Trades        int
	Wins          int
	Losses        int
	RealizedPnL   float64
	UnrealizedPnL float64
}

type Sniper struct {
	ID                string
	Detail            symbol.Symbol
	Strategy          Strategy
	State             strategy.StrategyState
	ExecutionPolicy   strategy.ExecutionPolicy
	ActiveOrders      []*order.Order
	Logger            *slog.Logger
	mu                sync.Mutex
	lifecycle         LifecycleState
	AccountType       order.AccountType
	Exchange          order.ExchangeMarket
	MarginTradeType   order.MarginTradeType

	lastSignalReason string
	lastStatusLogAt  time.Time
}

func NewSniper(id string, detail symbol.Symbol, strategy Strategy, policy strategy.ExecutionPolicy, exchange order.ExchangeMarket, logger *slog.Logger) *Sniper {
	if logger == nil {
		logger = strategy.AnalysisLogger()
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Sniper{
		ID:                  id,
		Detail:              detail,
		Strategy:            strategy,
		ExecutionPolicy:     policy,
		ActiveOrders:        make([]*order.Order, 0),
		AccountType:         order.ACCOUNT_SPECIAL,
		Exchange:            exchange,
		MarginTradeType:     order.TRADE_TYPE_GENERAL_DAY,
		Logger:              logger,
		lifecycle:           LifecycleActive,
	}
}

func (s *Sniper) Tick(obs Observation) Bullet {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := obs.Tick.CurrentPriceTime
	if now.IsZero() {
		now = time.Now()
	}

	if s.lifecycle == LifecycleStopped {
		return Bullet{}
	}

	// --- 1. 管理対象の状態整理 ---
	var reconciled []*order.Order
	var hasProcessingTrade bool
	var blockingOrder *order.Order

	for _, curr := range s.ActiveOrders {
		if curr.IsCompleted() {
			// 🌟 親注文が約定完了している場合、子注文(IfDone)があれば追跡を開始する
			if curr.Status == order.ORDER_STATUS_FILLED && curr.IfDone != nil {
				fmt.Printf("🎯 [%s] 子注文(IFD)の追跡を開始します: %s\n", s.Detail.Code, curr.IfDone.ID)
				// 子注文を管理対象に加える（IfDoneポインタを消すことで二重追加を防止）
				child := curr.IfDone
				curr.IfDone = nil 
				reconciled = append(reconciled, child)
				hasProcessingTrade = true
			}
			continue
		}
		reconciled = append(reconciled, curr)
		hasProcessingTrade = true

		if s.ExecutionPolicy != nil && !curr.IsPending() && curr.Status != order.ORDER_STATUS_CANCEL_SENT && !curr.IsCompleted() {
			s.ExecutionPolicy.ApplySyntheticFill(curr, obs.Tick)
		}

		if !curr.IsCompleted() {
			blockingOrder = curr
		}
	}
	s.ActiveOrders = reconciled

	// --- 2. 戦略判断の取得 ---
	currentPos := s.calculatePosition(obs.Positions)
	input := strategy.StrategyInput{
		Position:   currentPos,
		LatestTick: obs.Tick,
	}
	signal := s.Strategy.Evaluate(input)

	if signal.Price > 0 {
		signal.Price = s.Detail.RoundPrice(signal.Price)
	}

	// ライフサイクル管理
	if s.lifecycle == LifecycleExiting {
		holdQty := input.HoldQty()
		if holdQty == 0 {
			signal.Action = brain.ACTION_HOLD
		} else if !hasProcessingTrade {
			signal = s.buildForceExitSignal(holdQty)
		}
	}

	if signal.Reason != "" {
		s.lastSignalReason = signal.Reason
	}

	s.logStatus(obs, input)

	// --- 3. 整合性チェック（Reconciliation） ---
	for _, curr := range s.ActiveOrders {
		if curr == nil || curr.IsPending() || curr.Status == order.ORDER_STATUS_CANCEL_SENT {
			continue
		}

		isStillDesired := s.ExecutionPolicy.IsOrderDesired(curr, signal, s.Detail)
		if !isStillDesired {
			if curr.Status == order.ORDER_STATUS_IN_PROGRESS || curr.Status == order.ORDER_STATUS_FILL_EXPECTED {
				fmt.Printf("🔄 [%s] 戦略不整合により注文(%s)をキャンセルします [Status:%v]\n", s.Detail.Code, curr.ID, curr.Status)
				curr.Status = order.ORDER_STATUS_CANCEL_SENT
				curr.CancelSentAt = time.Now()
				return Bullet{CancelOrderID: curr.ID, Order: curr}
			}
		}
	}

	// --- 4. 新規トレードの開始 ---
	if signal.Action == brain.ACTION_HOLD || signal.Action == "" {
		return Bullet{}
	}

	if blockingOrder != nil {
		return Bullet{}
	}

	if hasProcessingTrade {
		return Bullet{}
	}

	entry, exit := s.buildOrderPair(obs, signal)
	s.ActiveOrders = append(s.ActiveOrders, entry)
	if exit != nil {
		entry.IfDone = exit
	}

	entry.CreatedAt = now
	_, req := s.wrapRequest(entry)

	return Bullet{Order: entry, Request: &req}
}

func (s *Sniper) buildOrderPair(obs Observation, signal brain.Signal) (*order.Order, *order.Order) {
	marketAction, _ := signal.Action.ToMarketAction()

	var closePositions []order.ClosePosition
	if marketAction == order.ACTION_SELL {
		closePositions, _ = s.matchPositionsToClose(obs, signal.Quantity)
	}
	entry := order.NewOrder(order.GenerateLocalID(), s.Detail.Code, marketAction, signal.Price, signal.Quantity)
	entry.InternalState = order.STATE_PENDING
	entry.ClosePositions = closePositions
	entry.Reason = signal.Reason // 🌟 理由を記録

	currentPos := s.calculatePosition(obs.Positions)
	simulatedInput := strategy.StrategyInput{
		Position:   s.simulateSignal(currentPos, signal),
		LatestTick: obs.Tick,
	}
	ifDoneSignal := s.Strategy.IfDone(simulatedInput, signal)

	var exit *order.Order
	if ifDoneSignal.Action != brain.ACTION_HOLD {
		exitAction, _ := ifDoneSignal.Action.ToMarketAction()
		exitPrice := ifDoneSignal.Price
		if exitPrice > 0 {
			exitPrice = s.Detail.RoundPrice(exitPrice)
		}
		exit = order.NewOrder(order.GenerateLocalID(), s.Detail.Code, exitAction, exitPrice, signal.Quantity)
		exit.InternalState = order.STATE_PREPARING
		exit.Reason = ifDoneSignal.Reason // 🌟 IFD注文の理由も記録
	}

	return entry, exit
}


func (s *Sniper) simulateSignal(currentPos strategy.Position, sig brain.Signal) strategy.Position {
	if sig.Action == brain.ACTION_HOLD {
		return currentPos
	}
	newQty := currentPos.Qty
	newTotalCost := currentPos.AveragePrice * currentPos.Qty
	switch sig.Action {
	case brain.ACTION_BUY:
		newQty += sig.Quantity
		newTotalCost += sig.Price * sig.Quantity
	case brain.ACTION_SELL:
		newQty -= sig.Quantity
	}
	newAvgPrice := 0.0
	if newQty > 0 {
		newAvgPrice = newTotalCost / newQty
	}
	return strategy.Position{Qty: newQty, AveragePrice: newAvgPrice}
}

func (s *Sniper) buildForceExitSignal(qty float64) brain.Signal {
	action := brain.ACTION_SELL
	if qty < 0 {
		action = brain.ACTION_BUY
		qty = -qty
	}
	return brain.Signal{Action: action, Price: 0, Quantity: qty, Reason: "LIFECYCLE_FORCE_EXIT"}
}

func (s *Sniper) logStatus(obs Observation, input strategy.StrategyInput) {
	if time.Since(s.lastStatusLogAt) < 1*time.Second {
		return
	}
	var orderDetails []string
	for _, curr := range s.ActiveOrders {
		if curr != nil {
			orderDetails = append(orderDetails, fmt.Sprintf("%s:%s Status:%d", curr.ID, curr.Action, curr.Status))
		}
	}
	s.Logger.Info("STRATEGY_STATUS",
		slog.String("symbol", s.Detail.Code),
		slog.Float64("price", obs.Tick.Price),
		slog.Float64("hold_qty", input.HoldQty()),
		slog.Any("orders", orderDetails),
	)
	s.lastStatusLogAt = time.Now()
}

func (s *Sniper) calculatePosition(groundPositions []position.Position) strategy.Position {
	var totalQty float64
	var totalCost float64
	for _, p := range groundPositions {
		totalQty += p.LeavesQty
		totalCost += p.Price * p.LeavesQty
	}
	for _, curr := range s.ActiveOrders {
		if curr != nil && curr.Status == order.ORDER_STATUS_FILL_EXPECTED {
			switch curr.Action {
			case order.ACTION_BUY:
				totalQty += curr.OrderQty
				totalCost += curr.OrderPrice * curr.OrderQty
			case order.ACTION_SELL:
				totalQty -= curr.OrderQty
			}
		}
	}
	avgPrice := 0.0
	if totalQty > 0 {
		avgPrice = totalCost / totalQty
	}
	return strategy.Position{Qty: totalQty, AveragePrice: avgPrice}
}

func (s *Sniper) wrapRequest(o *order.Order) (*order.Order, order.OrderRequest) {
	reqType := order.ORDER_TYPE_MARKET
	if o.OrderPrice > 0 {
		reqType = order.ORDER_TYPE_LIMIT
	}
	closeOrder := order.CLOSE_POSITION_ORDER_NONE
	if o.Action == order.ACTION_SELL && len(o.ClosePositions) == 0 {
		closeOrder = order.CLOSE_POSITION_ASC_DAY_DEC_PL
	}
	req := order.NewOrderRequest(s.Exchange, order.SECURITY_TYPE_STOCK, s.MarginTradeType, s.AccountType, closeOrder, o.ClosePositions, reqType)
	return o, req
}

func (s *Sniper) matchPositionsToClose(obs Observation, qty float64) ([]order.ClosePosition, order.ClosePositionOrder) {
	var closePositions []order.ClosePosition
	remainingSellQty := qty
	for _, p := range obs.Positions {
		if remainingSellQty <= 0 {
			break
		}
		closeQty := p.LeavesQty
		if closeQty > remainingSellQty {
			closeQty = remainingSellQty
		}
		closePositions = append(closePositions, order.ClosePosition{HoldID: p.ExecutionID, Qty: closeQty})
		remainingSellQty -= closeQty
	}
	return closePositions, order.CLOSE_POSITION_ORDER_NONE
}

func (s *Sniper) FailSendingOrder(ord *order.Order) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, o := range s.ActiveOrders {
		if o == ord {
			s.ActiveOrders = append(s.ActiveOrders[:i], s.ActiveOrders[i+1:]...)
			break
		}
	}
}

func (s *Sniper) UpdateOrderID(ord *order.Order, newID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.ActiveOrders {
		if o == ord || o.ID == ord.ID {
			o.ID = newID
			break
		}
	}
}

func (s *Sniper) RevertOrderStatus(ord *order.Order, status order.OrderStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.ActiveOrders {
		if o == ord || o.ID == ord.ID {
			o.Status = status
			break
		}
	}
}

func (s *Sniper) OrderlyExit() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lifecycle = LifecycleExiting
	s.Logger.Warn("LIFECYCLE_EXIT_TRIGGERED", slog.String("symbol", s.Detail.Code))
}

func (s *Sniper) ForceStop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lifecycle = LifecycleStopped
	s.Logger.Error("LIFECYCLE_STOP_TRIGGERED", slog.String("symbol", s.Detail.Code))
}

func (s *Sniper) GetLifecycle() LifecycleState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lifecycle
}

func (s *Sniper) ForceExit() {
	s.ForceStop()
	fmt.Printf("🚨 [%s] 強制停止モードON。\n", s.Detail.Code)
}

