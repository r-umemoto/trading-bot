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
	Orders            []*order.Order // 自身が管理する注文（FILL_EXPECTED等のローカル状態を含む）
	Logger            *slog.Logger
	mu                sync.Mutex
	lifecycle         LifecycleState
	AccountType       order.AccountType
	Exchange          order.ExchangeMarket
	MarginTradeType   order.MarginTradeType
	processedExecutions map[string]bool // 🌟 処理済みの約定ID（IFD二重発火防止用）

	lastSignalReason string
	lastStatusLogAt  time.Time
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
		Detail:          detail,
		Strategy:        strategy,
		ExecutionPolicy: policy,
		Orders:          make([]*order.Order, 0),
		AccountType:     order.ACCOUNT_SPECIAL,
		Exchange:        exchange,
		MarginTradeType: order.TRADE_TYPE_GENERAL_DAY,
		Logger:          logger,
		lifecycle:       LifecycleActive,
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

	// 1. 管理対象の注文を特定
	var activeBuyOrder *order.Order
	var activeSellOrder *order.Order
	for _, o := range s.Orders {
		if o.IsCompleted() || o.Status == order.ORDER_STATUS_CANCEL_SENT {
			continue
		}
		switch o.Action {
		case order.ACTION_BUY:
			activeBuyOrder = o
		case order.ACTION_SELL:
			activeSellOrder = o
		}
	}

	// 1.5 疑似約定 (Synthetic Fill) の判定
	if s.ExecutionPolicy != nil {
		if activeBuyOrder != nil && !activeBuyOrder.IsPending() {
			s.ExecutionPolicy.ApplySyntheticFill(activeBuyOrder, obs.Tick)
		}
		if activeSellOrder != nil && !activeSellOrder.IsPending() {
			s.ExecutionPolicy.ApplySyntheticFill(activeSellOrder, obs.Tick)
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
			// ノーポジション時は一切の新規エントリーを禁止
			signal.Action = brain.ACTION_HOLD
		} else {
			// ポジション保有時は、戦略のロジックが何であれ、安全かつ確実にポジションを決済する（成行決済）
			if holdQty > 0 {
				signal.Action = brain.ACTION_SELL
				signal.Price = 0 // 成行
				signal.Quantity = holdQty
				signal.Reason = "LIFECYCLE_FORCE_EXIT"
			} else {
				signal.Action = brain.ACTION_BUY
				signal.Price = 0 // 成行
				signal.Quantity = -holdQty
				signal.Reason = "LIFECYCLE_FORCE_EXIT"
			}
		}
	}

	if signal.Reason != "" {
		s.lastSignalReason = signal.Reason
	}

	// 🌟 定期的なステータスログ (分析用: 1秒に1回程度)
	if time.Since(s.lastStatusLogAt) > 1*time.Second {
		var orderDetails []string
		for _, o := range s.Orders {
			if !o.IsCompleted() {
				orderDetails = append(orderDetails, fmt.Sprintf("%s:%s@%.1f(%.0f)", o.ID, o.Action, o.OrderPrice, o.OrderQty))
			}
		}

		s.Logger.Info("STRATEGY_STATUS",
			slog.String("symbol", s.Detail.Code),
			slog.String("strategy_name", s.Strategy.Name()),
			slog.String("event", "STRATEGY_STATUS"),
			slog.Float64("price", obs.Tick.Price),
			slog.Float64("hold_qty", input.HoldQty()),
			slog.Any("orders", orderDetails),
		)
		s.lastStatusLogAt = time.Now()
	}

	// 現在の判断に関係する注文を特定
	var activeOrder *order.Order
	marketAction, _ := signal.Action.ToMarketAction()
	switch marketAction {
	case order.ACTION_BUY:
		activeOrder = activeBuyOrder
	case order.ACTION_SELL:
		activeOrder = activeSellOrder
	}

	// --- 4. 同期（Reconciliation）フェーズ ---

	// すでに注文が出ている場合
	if activeOrder != nil {
		// IDがまだ確定していない（PENDING）場合は、次のアクションを起こさず待機
		if activeOrder.IsPending() {
			return Bullet{}
		}

		// すでにキャンセル送信済みの場合は、その確定を待つ
		if activeOrder.Status == order.ORDER_STATUS_CANCEL_SENT {
			// ここに来るということは、上記のループで skip されなかった場合（二重キャンセル防止など）
			return Bullet{}
		}

		// 現在の注文が戦略の意図（シグナル）と一致しているかチェック
		isStillDesired := false
		if signal.Action != brain.ACTION_HOLD {
			isStillDesired = s.ExecutionPolicy.IsOrderDesired(activeOrder, signal, s.Detail)
		}

		// 意図と異なる、または HOLD になった場合はキャンセルを要求
		if !isStillDesired {
			// 【重要】疑似約定済みの場合は、キャンセルを禁止して約定確定を待つ
			// これにより、価格の微細な変化による「キャンセル・再発注スパムループ」を防止する
			if activeOrder.Status == order.ORDER_STATUS_FILL_EXPECTED {
				return Bullet{}
			}

			fmt.Printf("🔄 [%s] 意図と異なる注文(%s: %s@%.1f)をキャンセル要求します (Signal: %s@%.1f, Qty: %.1f)\n",
				s.Detail.Code, activeOrder.ID, activeOrder.Action, activeOrder.OrderPrice, signal.Action, signal.Price, signal.Quantity)
			activeOrder.Status = order.ORDER_STATUS_CANCEL_SENT
			activeOrder.CancelSentAt = time.Now()
			return Bullet{CancelOrderID: activeOrder.ID, Order: activeOrder}
		}

		// 一致している場合は維持
		return Bullet{}
	}

	if signal.Action == brain.ACTION_HOLD {
		return Bullet{}
	}

	marketAction, err := signal.Action.ToMarketAction()
	if err != nil {
		return Bullet{}
	}

	// --- 5. 新規発注フェーズ ---
	// 未完了注文がない場合（または既存の注文がキャンセル送信中の場合）に、新規発注を検討する
	// ただし、同じアクション（BUY/BUY, SELL/SELL）の注文が一つでも残っている場合は、
	// 二重発注を避けるためにその完了（または確定）を絶対に待つ。
	for _, o := range s.Orders {
		if !o.IsCompleted() {
			if o.Action == marketAction {
				// すでに同じ方向の注文が（PENDING/WAITING/CANCEL_SENT問わず）存在する場合は、
				// 重複発注を避けるため、次のアクションを絶対に起こさない。
				if o.Status == order.ORDER_STATUS_CANCEL_SENT {
					// キャンセル送信から一定時間（例：30秒）経過しても応答がない場合はゾンビとみなし、
					// ブロックを解除して新規発注フローへの進行を許可する。
					if !o.CancelSentAt.IsZero() && now.Sub(o.CancelSentAt) > 30*time.Second {
						fmt.Printf("⚠️ [%s] キャンセル送信から30秒以上経過しても応答がありません(ID:%s)。ゾンビとみなしてブロックを解除します。\n", s.Detail.Code, o.ID)
						continue
					}
					// キャンセルがAPI側で確定するのを待つ
					return Bullet{}
				}
				return Bullet{}
			}
		}
	}

	orderType := order.ORDER_TYPE_MARKET
	if signal.Price > 0 {
		orderType = order.ORDER_TYPE_LIMIT
	} else if signal.OrderType != 0 {
		orderType = signal.OrderType
	}

	// 決済注文（売り）の場合のポジション指定ロジック
	var closePositions []order.ClosePosition
	closeOrder := order.CLOSE_POSITION_ORDER_NONE
	if marketAction == order.ACTION_SELL {
		closePositions, closeOrder = s.matchPositionsToClose(obs, signal.Quantity)
	}

	// --- 6. IFD (If-Done) 注文の組み立て ---
	simulatedInput := strategy.StrategyInput{
		Position:   s.simulateSignal(currentPos, signal),
		LatestTick: obs.Tick,
	}
	ifDoneSignal := s.Strategy.IfDone(simulatedInput, signal)
	if ifDoneSignal.Price > 0 {
		ifDoneSignal.Price = s.Detail.RoundPrice(ifDoneSignal.Price)
	}
	var marketIFDAction order.Action
	var hasIFD bool
	if ifDoneSignal.Action != brain.ACTION_HOLD {
		marketIFDAction, _ = ifDoneSignal.Action.ToMarketAction()
		hasIFD = true
	}

	// Order/Request を作成する
	ptr, orderReq := s.buildOrderRequest(marketAction, signal.Price, signal.Quantity, orderType, closePositions, closeOrder)
	ptr.ClosePositions = closePositions

	ptr.HasIFD = hasIFD
	ptr.IFDAction = marketIFDAction
	ptr.IFDPrice = ifDoneSignal.Price
	ptr.IFDOrderType = ifDoneSignal.OrderType

	ptr.CreatedAt = obs.Tick.CurrentPriceTime // 🌟 約定レイテンシ計算のためにTick時刻に合わせる

	s.Orders = append(s.Orders, ptr)

	return Bullet{Order: ptr, Request: &orderReq}
}

// matchPositionsToClose は指定した数量分だけ、保有している建玉を FIFO（先入先出）で返済用に対対象指定します
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

// buildOrderRequest は注文情報のエンティティ生成とインフラ用リクエストパラメータ作成をカプセル化して行います
func (s *Sniper) buildOrderRequest(action order.Action, price float64, qty float64, orderType order.OrderType, closePositions []order.ClosePosition, closeOrder order.ClosePositionOrder) (*order.Order, order.OrderRequest) {
	ptr := order.NewOrder(order.GenerateLocalID(), s.Detail.Code, action, price, qty)
	ptr.InternalState = order.STATE_PENDING // 生成時は API 送信待ち (PENDING)

	reqType := order.ORDER_TYPE_MARKET
	if price > 0 {
		reqType = order.ORDER_TYPE_LIMIT
	} else if orderType != 0 {
		reqType = orderType
	}

	orderReq := order.NewOrderRequest(
		s.Exchange,
		order.SECURITY_TYPE_STOCK,
		order.TRADE_TYPE_GENERAL_DAY,
		order.ACCOUNT_SPECIAL,
		closeOrder,
		closePositions,
		reqType,
	)

	return ptr, orderReq
}

// calculatePosition は現在の注文状態と確定ポジションから、戦略に渡すための要約ポジションを計算します
func (s *Sniper) calculatePosition(groundPositions []position.Position) strategy.Position {
	var totalQty float64
	var totalCost float64

	// 1. API確定ポジションをベースにする
	for _, p := range groundPositions {
		totalQty += p.LeavesQty
		totalCost += p.Price * p.LeavesQty
	}

	// 2. 自身の管理注文のうち、疑似約定(FILL_EXPECTED)を先行計上
	for _, o := range s.Orders {
		if o.Status == order.ORDER_STATUS_FILL_EXPECTED {
			switch o.Action {
			case order.ACTION_BUY:
				totalQty += o.OrderQty
				totalCost += o.OrderPrice * o.OrderQty
			case order.ACTION_SELL:
				totalQty -= o.OrderQty
			}
		}
	}

	avgPrice := 0.0
	if totalQty > 0 {
		avgPrice = totalCost / totalQty
	}

	return strategy.Position{
		Qty:          totalQty,
		AveragePrice: avgPrice,
	}
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
		// 売りでコスト（平均単価）は変わらない（FIFO/移動平均の単純化）
	}

	newAvgPrice := 0.0
	if newQty > 0 {
		newAvgPrice = newTotalCost / newQty
	}

	return strategy.Position{
		Qty:          newQty,
		AveragePrice: newAvgPrice,
	}
}

// FailSendingOrder は発注失敗時に呼ばれ、Ordersリストから仮注文をクリアします
func (s *Sniper) FailSendingOrder(ord *order.Order) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, o := range s.Orders {
		if o == ord || o.ID == ord.ID {
			s.Orders = append(s.Orders[:i], s.Orders[i+1:]...)
			break
		}
	}
}

// RevertOrderStatus はキャンセル失敗時などに注文ステータスを安全に戻します
func (s *Sniper) RevertOrderStatus(ord *order.Order, status order.OrderStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, o := range s.Orders {
		if o == ord || o.ID == ord.ID {
			o.Status = status
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

// SyncOrders は事実（Observation）を自身の管理注文に反映し、必要ならIFD注文を発火させます。
func (s *Sniper) SyncOrders(obs Observation) Bullet {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := obs.Tick.CurrentPriceTime
	if now.IsZero() {
		now = time.Now()
	}

	var reconciledOrders []*order.Order
	var triggeredBullet Bullet

	for _, o := range s.Orders {
		// API側の状態（事実）を特定
		var hasMatch bool
		var extStatus order.OrderStatus
		var extCumQty float64
		var extExecs []order.Execution

		for _, ext := range obs.Orders {
			if o.ID == ext.ID {
				hasMatch = true
				extStatus = ext.Status
				extCumQty = ext.CumQty
				extExecs = ext.Executions
				break
			}
		}

		// 1. 状態の同期と約定のチェック
		var newExecs []order.Execution
		if hasMatch {
			o.Status = extStatus
			o.CumQty = extCumQty

			// 新しい約定があるかチェック
			for _, exec := range extExecs {
				if !s.processedExecutions[exec.ID] {
					newExecs = append(newExecs, exec)
				}
			}
		}

		// 2. 疑似約定(FILL_EXPECTED)のタイムアウト判定
		if o.Status == order.ORDER_STATUS_FILL_EXPECTED {
			if !o.Synthetic.ExpectedAt.IsZero() && now.Sub(o.Synthetic.ExpectedAt) > 20*time.Second {
				fmt.Printf("⚠️ [%s] 疑似約定タイムアウト: %s\n", s.Detail.Code, o.ID)
				o.Status = order.ORDER_STATUS_IN_PROGRESS
			}
		}

		// 3. IFD の発火判定
		if len(newExecs) > 0 {
			// 約定を「処理済み」としてマーク
			for _, exec := range newExecs {
				s.processedExecutions[exec.ID] = true
				o.AddExecution(exec) // 内部オブジェクトにも反映
			}

			// IFD 設定があれば決済注文を組み立てる
			if o.HasIFD && !triggeredBullet.HasOrder() {
				totalNewQty := 0.0
				var closePositions []order.ClosePosition
				for _, exec := range newExecs {
					totalNewQty += exec.Qty
					closePositions = append(closePositions, order.ClosePosition{
						HoldID: exec.ID,
						Qty:    exec.Qty,
					})
				}

				fmt.Printf("⚡ [%s] IFD発火: %.0f株の約定に対し、決済注文(%s)を準備します\n", s.Detail.Code, totalNewQty, o.IFDAction)
				ifdOrder, ifdReq := s.buildOrderRequest(
					o.IFDAction,
					o.IFDPrice,
					totalNewQty,
					o.IFDOrderType,
					closePositions,
					order.CLOSE_POSITION_ORDER_NONE,
				)
				ifdOrder.CreatedAt = now
				triggeredBullet = Bullet{Order: ifdOrder, Request: &ifdReq}
			}
		}

		// 4. 管理リストの整理（未完了、またはAPIにまだ存在する注文を保持）
		if !o.IsCompleted() || (hasMatch && o.FilledQty() < o.CumQty) {
			reconciledOrders = append(reconciledOrders, o)
		}
	}

	// 5. IFD注文があればリストに追加
	if triggeredBullet.HasOrder() {
		reconciledOrders = append(reconciledOrders, triggeredBullet.Order)
	}

	s.Orders = reconciledOrders
	return triggeredBullet
}
