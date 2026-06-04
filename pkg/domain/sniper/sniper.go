package sniper

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/brain"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
)

type Strategy interface {
	Name() string
	Evaluate(input strategy.StrategyInput) brain.Signal
	IfDone(input strategy.StrategyInput, prevSignal brain.Signal) brain.Signal
	AnalysisLogger() *slog.Logger
	// ShouldCancel は、現在アクティブな注文（未約定）をキャンセルすべきか戦略自身が判断します。
	ShouldCancel(input strategy.StrategyInput, ord *order.Order) bool
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

type Bullet interface {
	isBullet()
}

type OrderBullet struct {
	Order *order.Order
}

func (OrderBullet) isBullet() {}

type CancelBullet struct {
	OrderID string
}

func (CancelBullet) isBullet() {}

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
	Logger            *slog.Logger
	mu                sync.Mutex
	lifecycle         LifecycleState
	AccountType       order.AccountType
	Exchange          order.ExchangeMarket
	MarginTradeType   order.MarginTradeType

	lastSignalReason string
	lastStatusLogAt  time.Time
	lastCloseErrorAt time.Time
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
		return nil
	}

	// --- 1. 管理対象の状態整理 ---
	hasProcessingTrade := obs.HasProcessingTrade
	blockingOrder := obs.BlockingOrder

	// --- 2. 戦略判断の取得 ---
	currentPos := s.calculatePosition(obs)
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
	for _, curr := range obs.ActiveOrders {
		if curr == nil || curr.IsPending() || curr.IsCancelSent() {
			continue
		}

		if s.Strategy.ShouldCancel(input, curr) {
			if !curr.IsCompleted() {
				fmt.Printf("🔄 [%s] 戦略不整合により注文(%s)をキャンセルします [Status:%v]\n", s.Detail.Code, curr.ID, curr.Status())
				curr.ToCancelSent()
				curr.CancelSentAt = now
				return CancelBullet{OrderID: curr.ID}
			}
		}
	}

	// 送信キュー滞留中（PREPARING）の注文を探す
	var preparingOrder *order.Order
	for _, o := range obs.ActiveOrders {
		if o != nil && o.InternalState() == order.STATE_PREPARING {
			preparingOrder = o
			break
		}
	}

	// --- 4. 新規トレードの開始 ---
	if signal.Action == brain.ACTION_HOLD || signal.Action == "" {
		// もし送信キューに古い注文があり、最新シグナルが HOLD（不要）になったなら、古い注文をキャンセル（キューから削除）する
		if preparingOrder != nil {
			fmt.Printf("🔄 [%s] シグナル消滅により送信待ち注文(%s)を上書きキャンセルします\n", s.Detail.Code, preparingOrder.ID)
			return CancelBullet{OrderID: preparingOrder.ID}
		}
		return nil
	}

	if blockingOrder != nil {
		return nil
	}

	if hasProcessingTrade {
		return nil
	}

	// すでに送信待ち（PREPARING）の注文がある場合
	if preparingOrder != nil {
		marketAction, _ := signal.Action.ToMarketAction()
		roundedPrice := s.Detail.RoundPrice(signal.Price)
		// 最新のシグナルとキューの注文内容が完全に同じであれば、そのまま送信させるために何もしない（ブロック）
		if preparingOrder.Action == marketAction &&
			preparingOrder.OrderPrice == roundedPrice &&
			preparingOrder.OrderQty == signal.Quantity {
			return nil
		}
		
		// 内容が異なる場合は、古い注文を明示的にキャンセルするのではなく、
		// 直接新しい注文を生成して返すことで、インフラ層（Dispatcher）の Submit 上書き処理に委ねる
		fmt.Printf("🔄 [%s] シグナル変更により送信待ち注文(%s)を上書きします (旧: Price %.1f/Qty %.0f -> 新: Price %.1f/Qty %.0f)\n",
			s.Detail.Code, preparingOrder.ID, preparingOrder.OrderPrice, preparingOrder.OrderQty, roundedPrice, signal.Quantity)
	}

	// 返済エラー直後は、建玉の反映を待つためにクールダウンを設ける（1秒）
	isExit := (signal.TradeType == brain.TradeExit)
	if isExit && time.Since(s.lastCloseErrorAt) < 1*time.Second {
		s.Logger.Warn("⏳ 前回の返済エラーから1秒未満のため、返済注文の発注を一時見合わせます（建玉反映待ち）",
			slog.String("symbol", s.Detail.Code),
		)
		return nil
	}

	entry, exit := s.buildOrderPair(obs, signal)
	if exit != nil {
		entry.IfDone = exit
	}

	entry.CreatedAt = now

	return OrderBullet{Order: entry}
}

func (s *Sniper) buildOrderPair(obs Observation, signal brain.Signal) (*order.Order, *order.Order) {
	marketAction, _ := signal.Action.ToMarketAction()

	var closePositions []order.ClosePosition
	isExit := (signal.TradeType == brain.TradeExit)

	if isExit {
		closePositions, _ = s.matchPositionsToClose(obs, marketAction, signal.Quantity)
	}

	entryReq := &order.OrderRequest{
		Exchange:        s.Exchange,
		SecurityType:    order.SECURITY_TYPE_STOCK,
		MarginTradeType: s.MarginTradeType,
		AccountType:     s.AccountType,
	}
	cashMargin := order.CASH_MARGIN_MARGIN_ENTRY
	if isExit {
		cashMargin = order.CASH_MARGIN_MARGIN_EXIT
		entryReq.ClosePositions = closePositions
		if len(closePositions) == 0 {
			entryReq.ClosePositionOrder = order.CLOSE_POSITION_ASC_DAY_DEC_PL
		}
	}

	entry := order.NewOrder(
		order.GenerateLocalID(),
		s.Detail.Code,
		marketAction,
		signal.Price,
		signal.Quantity,
		order.WithType(signal.OrderType),
		order.WithCashMargin(cashMargin),
		order.WithRequest(entryReq),
		order.WithReason(signal.Reason),
	)
	entry.ToPending()

	currentPos := s.calculatePosition(obs)
	simulatedInput := strategy.StrategyInput{
		Position:   currentPos.Simulate(signal, obs.Tick.Price),
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

		exitReq := &order.OrderRequest{
			Exchange:           s.Exchange,
			SecurityType:       order.SECURITY_TYPE_STOCK,
			MarginTradeType:    s.MarginTradeType,
			AccountType:        s.AccountType,
			ClosePositionOrder: order.CLOSE_POSITION_ASC_DAY_DEC_PL,
		}

		exit = order.NewOrder(
			order.GenerateLocalID(),
			s.Detail.Code,
			exitAction,
			exitPrice,
			signal.Quantity,
			order.WithType(ifDoneSignal.OrderType),
			order.WithCashMargin(order.CASH_MARGIN_MARGIN_EXIT),
			order.WithRequest(exitReq),
			order.WithReason(ifDoneSignal.Reason),
		)
	}

	return entry, exit
}



func (s *Sniper) buildForceExitSignal(qty float64) brain.Signal {
	action := brain.ACTION_SELL
	if qty < 0 {
		action = brain.ACTION_BUY
		qty = -qty
	}
	return brain.Signal{Action: action, Price: 0, Quantity: qty, OrderType: order.ORDER_TYPE_MARKET, Reason: "LIFECYCLE_FORCE_EXIT"}
}

func (s *Sniper) logStatus(obs Observation, input strategy.StrategyInput) {
	if time.Since(s.lastStatusLogAt) < 1*time.Second {
		return
	}
	var orderDetails []string
	for _, curr := range obs.ActiveOrders {
		if curr != nil {
			orderDetails = append(orderDetails, fmt.Sprintf("%s:%s Status:%d", curr.ID, curr.Action, curr.Status()))
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

func (s *Sniper) calculatePosition(obs Observation) strategy.Position {
	var totalQty float64
	var totalCost float64
	for _, p := range obs.Positions {
		if p.Action == order.ACTION_SELL {
			totalQty -= p.LeavesQty
			totalCost -= p.Price * p.LeavesQty
		} else {
			totalQty += p.LeavesQty
			totalCost += p.Price * p.LeavesQty
		}
	}
	for _, curr := range obs.ActiveOrders {
		if curr != nil && curr.IsFillExpected() {
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



func (s *Sniper) matchPositionsToClose(obs Observation, action order.Action, qty float64) ([]order.ClosePosition, order.ClosePositionOrder) {
	var closePositions []order.ClosePosition
	remainingQty := qty

	targetAction := order.ACTION_BUY
	if action == order.ACTION_BUY {
		targetAction = order.ACTION_SELL
	}

	for _, p := range obs.Positions {
		if p.Action != targetAction {
			continue
		}
		if remainingQty <= 0 {
			break
		}
		closeQty := p.LeavesQty
		if closeQty > remainingQty {
			closeQty = remainingQty
		}
		closePositions = append(closePositions, order.ClosePosition{HoldID: p.ExecutionID, Qty: closeQty})
		remainingQty -= closeQty
	}
	return closePositions, order.CLOSE_POSITION_ORDER_NONE
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

func (s *Sniper) GetSymbolCode() string {
	return s.Detail.Code
}

func (s *Sniper) GetID() string {
	return s.ID
}

func (s *Sniper) GetStrategyName() string {
	return s.Strategy.Name()
}



