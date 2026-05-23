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
	AnalysisLogger() *slog.Logger // 🌟 解析用ロガーを取得
}

type LifecycleState int

const (
	LifecycleActive LifecycleState = iota // 平常運転：エントリー(ENTRY)・決済(EXIT)の両方を許可
	LifecycleExiting                       // 撤収中：新規注文は禁止、保有建玉の決済(EXIT)のみ許可（強制決済誘導）
	LifecycleStopped                      // 完全停止：すべての発注・キャンセル処理を遮断（価格更新も無視）
)

// Bullet はスナイパーのアクションの実体（発射される弾丸）をカプセル化したものです
type Bullet struct {
	Order         *order.Order
	Request       *order.OrderRequest
	CancelOrderID string
}

// HasOrder は新規発注のアクションがあるかどうかを判定します
func (b Bullet) HasOrder() bool {
	return b.Order != nil && b.Request != nil
}

// HasCancel はキャンセルアクションがあるかどうかを判定します
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

// Sniper は戦略と執行ロジックを持ち、判断とアクション（Bullet）の生成を担います。
type Sniper struct {
	Detail            symbol.Symbol
	Strategy          Strategy
	Performance       Performance // 🌟 取引成績
	LatestObservation Observation // 🌟 最新の観測事実（レポート用）
	State             strategy.StrategyState
	ExecutionPolicy   strategy.ExecutionPolicy
	ManagedOrders     []*ManagedOrder // 🌟 自身が管理する論理注文（トレード単位）
	Logger            *slog.Logger
	mu                sync.Mutex
	lifecycle         LifecycleState
	AccountType       order.AccountType
	Exchange          order.ExchangeMarket
	MarginTradeType   order.MarginTradeType

	processedExecutions map[string]bool // 🌟 処理済みの約定ID（IFD二重発火防止用）
	lastSignalReason    string
	lastStatusLogAt     time.Time
}

// NewSniper は新しいスナイパーを生成します。
func NewSniper(detail symbol.Symbol, strategy Strategy, policy strategy.ExecutionPolicy, exchange order.ExchangeMarket, logger *slog.Logger) *Sniper {
	if logger == nil {
		logger = strategy.AnalysisLogger()
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Sniper{
		Detail:              detail,
		Strategy:            strategy,
		ExecutionPolicy:     policy,
		ManagedOrders:       make([]*ManagedOrder, 0),
		AccountType:         order.ACCOUNT_SPECIAL,
		Exchange:            exchange,
		MarginTradeType:     order.TRADE_TYPE_GENERAL_DAY,
		Logger:              logger,
		lifecycle:           LifecycleActive,
		processedExecutions: make(map[string]bool),
	}
}

// Tick は整理された事実（Observation）を受け取り、次に行うべきアクション（Bullet）を決定します。
func (s *Sniper) Tick(obs Observation) Bullet {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.LatestObservation = obs // レポート用に保存

	now := obs.Tick.CurrentPriceTime
	if now.IsZero() {
		now = time.Now()
	}

	// 完全停止状態なら、すべて無視
	if s.lifecycle == LifecycleStopped {
		return Bullet{}
	}

	// 1. 管理対象の注文を特定 & 状態の整理
	var activeEntryOrder *order.Order
	var activeExitOrder *order.Order
	var hasProcessingTrade bool // まだ完了していないトレードがあるか

	var reconciled []*ManagedOrder
	for _, m := range s.ManagedOrders {
		if m.IsCompleted() {
			continue
		}
		reconciled = append(reconciled, m)
		hasProcessingTrade = true

		// 🌟 疑似約定のタイムアウト判定
		curr := m.CurrentOrder()
		if curr != nil && curr.Status == order.ORDER_STATUS_FILL_EXPECTED {
			if !curr.Synthetic.ExpectedAt.IsZero() && now.Sub(curr.Synthetic.ExpectedAt) > 20*time.Second {
				fmt.Printf("⚠️ [%s] 疑似約定タイムアウト: %s\n", s.Detail.Code, curr.ID)
				curr.Status = order.ORDER_STATUS_IN_PROGRESS
			}
		}

		// 現在のアクティブ注文を特定（Tickでの判断用）
		if m.Status == StatusEntryActive {
			activeEntryOrder = m.Entry
		} else if m.Status == StatusExitActive {
			activeExitOrder = m.Exit
		}
	}
	s.ManagedOrders = reconciled

	// 1.5 疑似約定 (Synthetic Fill) の判定
	if s.ExecutionPolicy != nil {
		if activeEntryOrder != nil && !activeEntryOrder.IsPending() {
			s.ExecutionPolicy.ApplySyntheticFill(activeEntryOrder, obs.Tick)
		}
		if activeExitOrder != nil && !activeExitOrder.IsPending() {
			s.ExecutionPolicy.ApplySyntheticFill(activeExitOrder, obs.Tick)
		}
	}

	currentPos := s.calculatePosition(obs.Positions)
	input := strategy.StrategyInput{
		Position:   currentPos,
		LatestTick: obs.Tick,
	}

	// 4. 戦略の判断を仰ぐ
	signal := s.Strategy.Evaluate(input)

	// 🌟 カブステーションのトリガチェックエラー（呼び値違反）を防ぐため、計算価格を正しいTick Sizeに丸める
	if signal.Price > 0 {
		signal.Price = s.Detail.RoundPrice(signal.Price)
	}

	// 🌟 ライフサイクルに基づくゲートキーピング（撤収モード時の新規発注抑止および強制決済誘導）
	if s.lifecycle == LifecycleExiting {
		holdQty := input.HoldQty()
		if holdQty == 0 {
			signal.Action = brain.ACTION_HOLD
		} else if !hasProcessingTrade {
			// ポジション保有かつ進行中の論理注文がない場合は、安全に成行決済を誘導
			if holdQty > 0 {
				signal.Action = brain.ACTION_SELL
				signal.Price = 0
				signal.Quantity = holdQty
				signal.Reason = "LIFECYCLE_FORCE_EXIT"
			} else {
				signal.Action = brain.ACTION_BUY
				signal.Price = 0
				signal.Quantity = -holdQty
				signal.Reason = "LIFECYCLE_FORCE_EXIT"
			}
		}
	}

	if signal.Reason != "" {
		s.lastSignalReason = signal.Reason
	}

	// 🌟 定期的なステータスログ
	if time.Since(s.lastStatusLogAt) > 1*time.Second {
		var orderDetails []string
		for _, m := range s.ManagedOrders {
			curr := m.CurrentOrder()
			if curr != nil {
				orderDetails = append(orderDetails, fmt.Sprintf("%s:%s@%.1f(%.0f) Status:%d", curr.ID, curr.Action, curr.OrderPrice, curr.OrderQty, m.Status))
			}
		}

		s.Logger.Info("STRATEGY_STATUS",
			slog.String("symbol", s.Detail.Code),
			slog.String("strategy_name", s.Strategy.Name()),
			slog.Float64("price", obs.Tick.Price),
			slog.Float64("hold_qty", input.HoldQty()),
			slog.Any("orders", orderDetails),
		)
		s.lastStatusLogAt = time.Now()
	}

	// 現在の判断に関係する注文を特定
	var activeOrder *order.Order
	marketAction, _ := signal.Action.ToMarketAction()
	if activeEntryOrder != nil && activeEntryOrder.Action == marketAction {
		activeOrder = activeEntryOrder
	} else if activeExitOrder != nil && activeExitOrder.Action == marketAction {
		activeOrder = activeExitOrder
	}

	// --- 4. 同期（Reconciliation）フェーズ ---

	// すでに注文が出ている場合
	if activeOrder != nil {
		if activeOrder.IsPending() || activeOrder.Status == order.ORDER_STATUS_CANCEL_SENT {
			return Bullet{}
		}

		// 現在の注文が戦略の意図（シグナル）と一致しているかチェック
		isStillDesired := false
		if signal.Action != brain.ACTION_HOLD {
			isStillDesired = s.ExecutionPolicy.IsOrderDesired(activeOrder, signal, s.Detail)
		}

		if !isStillDesired {
			if activeOrder.Status == order.ORDER_STATUS_FILL_EXPECTED {
				return Bullet{}
			}
			fmt.Printf("🔄 [%s] 意図と異なる注文(%s)をキャンセル要求します\n", s.Detail.Code, activeOrder.ID)
			activeOrder.Status = order.ORDER_STATUS_CANCEL_SENT
			activeOrder.CancelSentAt = time.Now()
			return Bullet{CancelOrderID: activeOrder.ID, Order: activeOrder}
		}
		return Bullet{}
	}

	if signal.Action == brain.ACTION_HOLD {
		return Bullet{}
	}

	marketAction, err := signal.Action.ToMarketAction()
	if err != nil {
		return Bullet{}
	}

	// 新規トレードの開始（既存の同方向注文がないか最終チェック）
	for _, m := range s.ManagedOrders {
		curr := m.CurrentOrder()
		if curr != nil && curr.Action == marketAction && !curr.IsCompleted() {
			// 同じ方向の注文が（キャンセル送信中も含め）存在する場合は待つ
			if curr.Status == order.ORDER_STATUS_CANCEL_SENT {
				if !curr.CancelSentAt.IsZero() && now.Sub(curr.CancelSentAt) > 30*time.Second {
					continue
				}
				return Bullet{}
			}
			return Bullet{}
		}
	}

	// --- 5. 論理注文の組み立てと発注 ---
	managed := s.buildManagedOrder(obs, signal)
	s.ManagedOrders = append(s.ManagedOrders, managed)

	// 最初のエントリー注文を発射
	managed.Status = StatusEntryActive
	managed.Entry.CreatedAt = obs.Tick.CurrentPriceTime
	_, req := s.wrapRequest(managed.Entry)

	return Bullet{Order: managed.Entry, Request: &req}
}

// buildManagedOrder は戦略シグナルから論理注文（ManagedOrder）を組み立てます。
func (s *Sniper) buildManagedOrder(obs Observation, signal brain.Signal) *ManagedOrder {
	marketAction, _ := signal.Action.ToMarketAction()

	// エントリー注文の作成
	var closePositions []order.ClosePosition
	if marketAction == order.ACTION_SELL {
		closePositions, _ = s.matchPositionsToClose(obs, signal.Quantity)
	}
	entry := order.NewOrder(order.GenerateLocalID(), s.Detail.Code, marketAction, signal.Price, signal.Quantity)
	entry.InternalState = order.STATE_PENDING
	entry.ClosePositions = closePositions

	// 決済注文（IFDのDone部分）のシミュレーションと作成
	currentPos := s.calculatePosition(obs.Positions)
	simulatedInput := strategy.StrategyInput{
		Position:   s.simulateSignal(currentPos, signal),
		LatestTick: obs.Tick,
	}
	ifDoneSignal := s.Strategy.IfDone(simulatedInput, signal)

	var exit *order.Order
	if ifDoneSignal.Action != brain.ACTION_HOLD {
		exitAction, _ := ifDoneSignal.Action.ToMarketAction()
		if ifDoneSignal.Price > 0 {
			ifDoneSignal.Price = s.Detail.RoundPrice(ifDoneSignal.Price)
		}

		exit = order.NewOrder(order.GenerateLocalID(), s.Detail.Code, exitAction, ifDoneSignal.Price, signal.Quantity)
		exit.InternalState = order.STATE_PREPARING
	}

	managed := NewManagedOrder(entry.ID, entry, exit)
	return managed
}

// wrapRequest は生の Order オブジェクトを API 送信用の OrderRequest に包みます。
func (s *Sniper) wrapRequest(o *order.Order) (*order.Order, order.OrderRequest) {
	reqType := order.ORDER_TYPE_MARKET
	if o.OrderPrice > 0 {
		reqType = order.ORDER_TYPE_LIMIT
	}

	closeOrder := order.CLOSE_POSITION_ORDER_NONE
	if o.Action == order.ACTION_SELL && len(o.ClosePositions) == 0 {
		closeOrder = order.CLOSE_POSITION_ASC_DAY_DEC_PL
	}

	req := order.NewOrderRequest(
		s.Exchange,
		order.SECURITY_TYPE_STOCK,
		s.MarginTradeType,
		s.AccountType,
		closeOrder,
		o.ClosePositions,
		reqType,
	)
	return o, req
}

// SyncOrders は事実（Observation）を自身の管理注文に反映し、必要なら決済注文を即座に発火させます。
func (s *Sniper) SyncOrders(obs Observation) Bullet {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := obs.Tick.CurrentPriceTime
	if now.IsZero() {
		now = time.Now()
	}

	var triggeredBullet Bullet

	for _, m := range s.ManagedOrders {
		if m.IsCompleted() {
			continue
		}

		curr := m.CurrentOrder()
		var hasMatch bool
		var extStatus order.OrderStatus
		var extCumQty float64
		var extExecs []order.Execution

		for _, ext := range obs.Orders {
			if curr.ID == ext.ID {
				hasMatch = true
				extStatus = ext.Status
				extCumQty = ext.CumQty
				extExecs = ext.Executions
				break
			}
		}

		if hasMatch {
			curr.Status = extStatus
			curr.CumQty = extCumQty
			for _, exec := range extExecs {
				if !s.processedExecutions[exec.ID] {
					s.processedExecutions[exec.ID] = true
					curr.AddExecution(exec)
				}
			}
		}

		// 論理状態の遷移
		switch m.Status {
		case StatusEntryActive:
			if m.Entry.Status == order.ORDER_STATUS_FILLED {
				// エントリー全約定 -> 決済フェーズへ
				if m.Exit != nil {
					fmt.Printf("⚡ [%s] エントリー約定(%s) -> 決済注文(%s)を即時発射します\n", s.Detail.Code, m.Entry.ID, m.Exit.Action)
					m.Status = StatusExitPreparing
					m.Exit.CreatedAt = now
					m.Exit.InternalState = order.STATE_PENDING
					// 約定した建玉を決済対象として紐付け
					m.Exit.ClosePositions = make([]order.ClosePosition, 0)
					for _, exec := range m.Entry.Executions {
						m.Exit.ClosePositions = append(m.Exit.ClosePositions, order.ClosePosition{
							HoldID: exec.ID,
							Qty:    exec.Qty,
						})
					}
					_, req := s.wrapRequest(m.Exit)
					triggeredBullet = Bullet{Order: m.Exit, Request: &req}
					m.Status = StatusExitActive // 送信したとみなす
				} else {
					m.Status = StatusCompleted
				}
			} else if m.Entry.Status == order.ORDER_STATUS_CANCELED || m.Entry.Status == order.ORDER_STATUS_EXPIRED {
				m.Status = StatusCanceled
			}

		case StatusExitActive:
			if m.Exit.Status == order.ORDER_STATUS_FILLED {
				m.Status = StatusCompleted
			} else if m.Exit.Status == order.ORDER_STATUS_CANCELED || m.Exit.Status == order.ORDER_STATUS_EXPIRED {
				m.Status = StatusCanceled
			}
		}
	}

	return triggeredBullet
}

// calculatePosition は現在の注文状態と確定ポジションから、戦略に渡すための要約ポジションを計算します
func (s *Sniper) calculatePosition(groundPositions []position.Position) strategy.Position {
	var totalQty float64
	var totalCost float64

	for _, p := range groundPositions {
		totalQty += p.LeavesQty
		totalCost += p.Price * p.LeavesQty
	}

	// 疑似約定(FILL_EXPECTED)を先行計上
	for _, m := range s.ManagedOrders {
		curr := m.CurrentOrder()
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

// simulateSignal は特定のシグナルが約定したと仮定した場合のポジション状態をシミュレートします
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

// matchPositionsToClose は指定した数量分だけ、保有している建玉を FIFO で返済用に対象指定します
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
		closePositions = append(closePositions, order.ClosePosition{
			HoldID: p.ExecutionID,
			Qty:    closeQty,
		})
		remainingSellQty -= closeQty
	}
	closeOrder := order.CLOSE_POSITION_ORDER_NONE
	if len(closePositions) == 0 {
		closeOrder = order.CLOSE_POSITION_ASC_DAY_DEC_PL
	}
	return closePositions, closeOrder
}

// FailSendingOrder は発注失敗時に呼ばれ、Ordersリストから仮注文をクリアします
func (s *Sniper) FailSendingOrder(ord *order.Order) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, m := range s.ManagedOrders {
		if m.Entry == ord || (m.Exit != nil && m.Exit == ord) {
			s.ManagedOrders = append(s.ManagedOrders[:i], s.ManagedOrders[i+1:]...)
			break
		}
	}
}

// RevertOrderStatus はキャンセル失敗時などに注文ステータスを安全に戻します
func (s *Sniper) RevertOrderStatus(ord *order.Order, status order.OrderStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.ManagedOrders {
		curr := m.CurrentOrder()
		if curr == ord || curr.ID == ord.ID {
			curr.Status = status
			break
		}
	}
}

// OrderlyExit はスナイパーを撤収モードに移行させます。
func (s *Sniper) OrderlyExit() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lifecycle = LifecycleExiting
	s.Logger.Warn("LIFECYCLE_EXIT_TRIGGERED", slog.String("symbol", s.Detail.Code))
}

// ForceStop はスナイパーを完全停止モードに移行させます。
func (s *Sniper) ForceStop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lifecycle = LifecycleStopped
	s.Logger.Error("LIFECYCLE_STOP_TRIGGERED", slog.String("symbol", s.Detail.Code))
}

// GetLifecycle は現在のライフサイクル状態を安全に取得します。
func (s *Sniper) GetLifecycle() LifecycleState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lifecycle
}

// ForceExit は強制停止時に呼ばれます。
func (s *Sniper) ForceExit() {
	s.ForceStop()
	fmt.Printf("🚨 [%s] 強制停止モードON。\n", s.Detail.Code)
}

// CalcUnrealizedPnL は含み損益を計算します。
func (s *Sniper) CalcUnrealizedPnL(obs Observation) float64 {
	var unrealized float64
	for _, p := range obs.Positions {
		unrealized += (obs.Tick.Price - p.Price) * p.LeavesQty
	}
	return unrealized
}
