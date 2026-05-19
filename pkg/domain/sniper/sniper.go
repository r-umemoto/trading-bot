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
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
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

// Sniper は戦略とAPIクライアントを持ち、執行を担います
type Sniper struct {
	Detail          symbol.Symbol // 🌟 銘柄詳細（Symbol, PriceRangeGroup等を含む）
	positions       []position.Position
	Performance     Performance // 🌟 追加
	Strategy        Strategy
	State           strategy.StrategyState   // 👈 銘柄ごとの戦略ステート
	ExecutionPolicy strategy.ExecutionPolicy // 👈 執行ポリシー（疑似約定判定）
	Orders          []*order.Order
	Logger          *slog.Logger // 🌟 解析用ロガー
	mu              sync.Mutex   // 👈 状態をロックするための鍵
	lifecycle       LifecycleState // 👈 ライフサイクル状態遷移
	AccountType     order.AccountType
	Exchange        order.ExchangeMarket
	MarginTradeType order.MarginTradeType

	processedExecutions map[string]bool // 🌟 処理済みの約定IDを記録
	lastSignalReason    string          // 🌟 最新のシグナル理由（分析用）
	lastStatusLogAt     time.Time       // 🌟 最後にステータスログを出力した時刻
	dataPool            tick.DataPool   // 👈 データプールへの参照
}

// NewSniper は新しいスナイパーを生成します
func NewSniper(detail symbol.Symbol, strategy Strategy, policy strategy.ExecutionPolicy, exchange order.ExchangeMarket, logger *slog.Logger, dataPool tick.DataPool) *Sniper {
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
		Orders:              make([]*order.Order, 0),
		positions:           []position.Position{}, // 初期状態は空
		AccountType:         order.ACCOUNT_SPECIAL,
		Exchange:            exchange,
		MarginTradeType:     order.TRADE_TYPE_GENERAL_DAY,
		processedExecutions: make(map[string]bool),
		Logger:              logger,
		lifecycle:           LifecycleActive,
		dataPool:            dataPool,
	}
}

// 価格の更新がされたと時に実行される監視ロジック
func (s *Sniper) Tick() Bullet {
	// 処理中は他のゴルーチンが状態を触れないようにロック！
	s.mu.Lock()
	defer s.mu.Unlock() // 関数が終わったら必ずロック解除

	// --- 0. クリーンアップフェーズ ---
	s.cleanupZombiesLocked()

	// 0. 呼値を最新の価格に基づいて更新
	state := s.dataPool.GetState(s.Detail.Code)

	// 完全停止状態なら、すべて無視
	if s.lifecycle == LifecycleStopped {
		return Bullet{}
	}

	// 1. 管理対象の注文を特定する（アクションに合わせた注文を抽出）
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

	// 1.5 疑似約定 (Synthetic Fill) の判定 (両方の注文に対して実施)
	// PENDING（送信待ち/API受付待ち）の注文は板に並んでいないため疑似約定判定から除外する
	if s.ExecutionPolicy != nil {
		if activeBuyOrder != nil && !activeBuyOrder.IsPending() {
			s.ExecutionPolicy.ApplySyntheticFill(activeBuyOrder, state.LatestTick)
		}
		if activeSellOrder != nil && !activeSellOrder.IsPending() {
			s.ExecutionPolicy.ApplySyntheticFill(activeSellOrder, state.LatestTick)
		}
	}

	input := strategy.StrategyInput{
		Orders:        strategy.StrategyOrders(s.Orders),
		LatestTick:    state.LatestTick,
		BasePositions: s.positions,
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
			slog.Float64("price", state.LatestTick.Price),
			slog.Float64("hold_qty", input.HoldQty()),
			slog.Any("orders", orderDetails),
			slog.Float64("realized_pnl", s.Performance.RealizedPnL),
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
			return Bullet{CancelOrderID: activeOrder.ID}
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
					if !o.CancelSentAt.IsZero() && time.Since(o.CancelSentAt) > 30*time.Second {
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
		closePositions, closeOrder = s.matchPositionsToClose(signal.Quantity)
	}

	// --- 6. IFD (If-Done) 注文の組み立て ---
	// 直前のシグナルが約定したと仮定して、次の意図を問う
	ifDoneSignal := s.Strategy.IfDone(input.SimulateSignal(signal), signal)
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

	ptr.HasIFD = hasIFD
	ptr.IFDAction = marketIFDAction
	ptr.IFDPrice = ifDoneSignal.Price
	ptr.IFDOrderType = ifDoneSignal.OrderType

	ptr.CreatedAt = state.LatestTick.CurrentPriceTime // 🌟 約定レイテンシ計算のためにTick時刻に合わせる

	s.Orders = append(s.Orders, ptr)

	return Bullet{Order: ptr, Request: &orderReq}
}

// matchPositionsToClose は指定した数量分だけ、保有している建玉を FIFO（先入先出）で返済用に対象指定します
func (s *Sniper) matchPositionsToClose(qty float64) ([]order.ClosePosition, order.ClosePositionOrder) {
	var closePositions []order.ClosePosition
	remainingSellQty := qty
	for _, p := range s.positions {
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

func (s *Sniper) cleanupZombiesLocked() {
	active := make([]*order.Order, 0, len(s.Orders))
	for _, o := range s.Orders {
		if o.IsCompleted() {
			continue
		}

		// 1. PENDING (API受付待ち) のタイムアウト (30秒)
		// 発注要求から30秒経っても確定しない場合は、通信失敗やAPIの受付拒否とみなす
		if o.IsPending() {
			if time.Since(o.CreatedAt) > 30*time.Second {
				fmt.Printf("⚠️ [%s] PENDING注文がタイムアウト(30s)したため削除します: %s\n", s.Detail.Code, o.ID)
				continue
			}
		}

		// 2. FILL_EXPECTED (疑似約定) のタイムアウト (20秒)
		// 本物の約定通知が来ない場合は、板の動きを見誤ったか貫通しなかったとみなして元に戻す
		if o.Status == order.ORDER_STATUS_FILL_EXPECTED {
			if !o.Synthetic.ExpectedAt.IsZero() && time.Since(o.Synthetic.ExpectedAt) > 20*time.Second {
				fmt.Printf("⚠️ [%s] 疑似約定から20秒経過しても通知がないため、待機状態に戻します: %s\n", s.Detail.Code, o.ID)
				o.Status = order.ORDER_STATUS_IN_PROGRESS
			}
		}

		active = append(active, o)
	}
	s.Orders = active
}

// FailSendingOrder は発注失敗時に呼ばれ、Ordersリストから仮注文をクリアします
func (s *Sniper) FailSendingOrder(ord *order.Order) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, o := range s.Orders {
		if o == ord {
			// 該当するポインタをリストから削除
			s.Orders = append(s.Orders[:i], s.Orders[i+1:]...)
			break
		}
	}
}

// OrderlyExit はスナイパーを撤収モードに移行させます。新規エントリーは禁止され、決済のみ許可されます。
func (s *Sniper) OrderlyExit() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lifecycle = LifecycleExiting
	s.Logger.Warn("LIFECYCLE_EXIT_TRIGGERED", slog.String("symbol", s.Detail.Code))
}

// ForceStop はスナイパーを完全停止モードに移行させます。これ以上の処理はすべて遮断されます。
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

// ForceExit はキルスイッチ作動時に呼ばれ、自身の未約定注文のキャンセルと成行決済を行います
func (s *Sniper) ForceExit() {
	s.ForceStop()
	fmt.Printf("🚨 [%s] 強制停止モードON。これ以降の価格更新は無視し、強制決済プロセスを開始します。\n", s.Detail.Code)
}

// reducePositions は、指定された数量分だけ古い建玉から順に削減し、損益を計算します
func (s *Sniper) reducePositions(sellQty float64, sellPrice float64, sellTime time.Time) {
	remainingToSell := sellQty
	var newPositions []position.Position
	var totalTradePnL float64       // 🌟 今回の「売り約定全体」の損益を合算する変数
	var earliestEntryTime time.Time // 🌟 保有時間の計算用

	for _, p := range s.positions {
		if remainingToSell <= 0 {
			// 売却分を消化しきったら、残りの建玉はそのまま保持リストへ
			newPositions = append(newPositions, p)
			continue
		}

		closeQty := p.LeavesQty
		if closeQty > remainingToSell {
			closeQty = remainingToSell
		}

		// 最も古い建玉の時間を取得
		if earliestEntryTime.IsZero() || (!p.Meta.EntryTime.IsZero() && p.Meta.EntryTime.Before(earliestEntryTime)) {
			earliestEntryTime = p.Meta.EntryTime
		}

		// 🌟 損益計算
		tradePnL := (sellPrice - p.Price) * closeQty
		totalTradePnL += tradePnL // 全体の損益に加算

		s.Performance.RealizedPnL += tradePnL
		s.Performance.Trades++
		if tradePnL > 0 {
			s.Performance.Wins++
		} else if tradePnL < 0 {
			s.Performance.Losses++
		}

		if p.LeavesQty <= remainingToSell {
			// この建玉ロットを全量売却するケース
			remainingToSell -= p.LeavesQty
			// 全量売却なので newPositions には追加しない（消滅）
		} else {
			// この建玉ロットの一部だけを売却するケース
			p.LeavesQty -= remainingToSell
			remainingToSell = 0
			newPositions = append(newPositions, p)
		}
	}

	// 🌟 アナライザー用のログ (ループの外で1回だけ出力！)
	holdTimeSec := 0.0
	if !earliestEntryTime.IsZero() && !sellTime.IsZero() {
		// 売り約定時刻から、最も古い建玉の取得時刻を引いて保有時間を算出
		holdTimeSec = sellTime.Sub(earliestEntryTime).Seconds()
	}
	s.Logger.Info("POSITION_CLOSED",
		slog.String("symbol", s.Detail.Code),
		slog.String("strategy_name", s.Strategy.Name()),
		slog.String("event", "POSITION_CLOSED"),
		slog.String("exit_reason", s.lastSignalReason), // 🌟 決済理由を記録
		slog.Float64("pnl", totalTradePnL),
		slog.Float64("hold_time_sec", holdTimeSec),
	)

	// 更新された建玉リストで上書き
	s.positions = newPositions
}

// CalcUnrealizedPnL は、現在の価格を基にスナイパーが保有する建玉の含み損益を計算します
func (s *Sniper) CalcUnrealizedPnL(currentPrice float64) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	var unrealized float64
	for _, p := range s.positions {
		unrealized += (currentPrice - p.Price) * p.LeavesQty
	}
	return unrealized
}

// SyncOrders はインフラ層から取得した最新の注文一覧と同期し、内部状態を「事実」に合わせます
func (s *Sniper) SyncOrders(externalOrders order.Orders) Bullet {
	s.mu.Lock()
	defer s.mu.Unlock()

	var newExecQty float64
	var triggeredIFDParent *order.Order
	var newExecs []order.Execution

	// 新しい管理リストを作成（事実ベースで再構築）
	var reconciledOrders []*order.Order

	// 1. InternalState が未確定（PENDING/PREPARING）または未完了の注文をまず保持する
	// APIの一覧に反映されるまでのタイムラグによる注文消失（および再発注スパム）を防ぐため。
	for _, o := range s.Orders {
		if o.IsPending() {
			// 仮ID（PENDING）はAPI未達の可能性があるため、30秒間は無条件で保持する
			if time.Since(o.CreatedAt) < 30*time.Second {
				reconciledOrders = append(reconciledOrders, o)
			} else {
				fmt.Printf("🗑️ [%s] APIに30秒以上現れない送信中注文(%s)をOrdersから削除します\n", s.Detail.Code, o.ID)
			}
		} else if !o.IsCompleted() {
			// 既にIDが確定している未完了注文は、APIの瞬断に備えて一旦リストに残す
			// （確定済み注文は30秒制限の対象外。指値でずっと待機している場合があるため）
			reconciledOrders = append(reconciledOrders, o)
		}
	}

	// 2. APIから取得した注文をすべて反映・採用する
	for _, ext := range externalOrders.Orders {
		if ext.Symbol != s.Detail.Code {
			continue
		}

		// 内部で管理している注文を探す
		var matchedInternal *order.Order
		for _, o := range s.Orders {
			if o.ID == ext.ID {
				matchedInternal = o
				break
			}
		}

		if matchedInternal == nil {
			// 【重要】自分が出した注文ではない（他の戦略の注文など）可能性があるため、一切関知しない。
			// 起動時の残存建玉は PositionCleaner が掃除するため、ここでは「自分がこのセッションで出した注文」のみを管理する。
			continue
		}

		// 状態の同期
		matchedInternal.Status = ext.Status
		matchedInternal.CumQty = ext.CumQty
		if matchedInternal.IsPending() {
			matchedInternal.InternalState = order.STATE_ACTIVE // API側に存在することが確認できたらACTIVEへ遷移
		}

		// 約定の反映
		for _, exec := range ext.Executions {
			if !s.processedExecutions[exec.ID] {
				s.applyExecution(exec, matchedInternal.Action, matchedInternal.CreatedAt)
				newExecQty += exec.Qty
				triggeredIFDParent = matchedInternal
				newExecs = append(newExecs, exec)
			}
		}

		// 完了していない注文を管理リストに残す
		isDataComplete := matchedInternal.FilledQty() >= matchedInternal.CumQty
		if !matchedInternal.IsCompleted() || !isDataComplete {
			exists := false
			for _, ro := range reconciledOrders {
				if ro.ID == matchedInternal.ID {
					exists = true
					break
				}
			}
			if !exists {
				reconciledOrders = append(reconciledOrders, matchedInternal)
			}
		}
	}

	// リストを更新（APIにない確定済みIDの注文はここで消える）
	s.Orders = reconciledOrders

	// 3. IFDの発火処理
	if newExecQty > 0 && triggeredIFDParent != nil && triggeredIFDParent.HasIFD {
		fmt.Printf("⚡ [%s] IFD発火: %.2f株の約定に伴い、決済注文を発注します\n", s.Detail.Code, newExecQty)

		var closePositions []order.ClosePosition
		if triggeredIFDParent.IFDAction == order.ACTION_SELL {
			for _, exec := range newExecs {
				closePositions = append(closePositions, order.ClosePosition{
					HoldID: exec.ID,
					Qty:    exec.Qty,
				})
			}
		}

		closeOrder := order.CLOSE_POSITION_ORDER_NONE
		if triggeredIFDParent.IFDAction == order.ACTION_SELL && len(closePositions) == 0 {
			closeOrder = order.CLOSE_POSITION_ASC_DAY_DEC_PL
		}

		ifdPendingOrder, orderReq := s.buildOrderRequest(
			triggeredIFDParent.IFDAction,
			triggeredIFDParent.IFDPrice,
			newExecQty,
			triggeredIFDParent.IFDOrderType,
			closePositions,
			closeOrder,
		)
		ifdPendingOrder.HasIFD = false
		ifdPendingOrder.CreatedAt = time.Now()

		s.Orders = append(s.Orders, ifdPendingOrder)
		return Bullet{Order: ifdPendingOrder, Request: &orderReq}
	}

	return Bullet{}
}

func (s *Sniper) applyExecution(exec order.Execution, action order.Action, orderCreatedAt time.Time) {
	// 🌟 重複チェック（冪等性の担保）
	if s.processedExecutions[exec.ID] {
		return
	}
	s.processedExecutions[exec.ID] = true

	switch action {
	case order.ACTION_BUY:
		s.positions = append(s.positions, position.Position{
			ExecutionID: exec.ID,
			Symbol:      s.Detail.Code,
			LeavesQty:   exec.Qty,
			Price:       exec.Price,
			Meta:        position.PositionMeta{EntryTime: exec.ExecutionTime}, // 🌟 メタデータとして記録
		})
		fmt.Printf("✅ [%s] 買付約定を反映: 単価%.2f 数量%f\n", s.Detail.Code, exec.Price, exec.Qty)

		queueTimeMs := exec.ExecutionTime.Sub(orderCreatedAt).Milliseconds()
		s.Logger.Info("FILLED",
			slog.String("symbol", s.Detail.Code),
			slog.String("strategy_name", s.Strategy.Name()),
			slog.String("event", "FILLED"),
			slog.String("entry_reason", s.lastSignalReason), // 🌟 エントリー理由を記録
			slog.Float64("qty", exec.Qty),
			slog.Float64("price", exec.Price),
			slog.Int64("queue_time_ms", queueTimeMs),
		)
	case order.ACTION_SELL:
		s.reducePositions(exec.Qty, exec.Price, exec.ExecutionTime)
		fmt.Printf("✅ [%s] 売付約定を反映: 数量%f\n", s.Detail.Code, exec.Qty)
	}
}

// OnExecution は廃止されました (SyncOrders に統合)
