package sniper

import (
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/position"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/brain"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

// ReportableTarget はレポート出力の対象となるエンティティのインターフェースです。
type ReportableTarget interface {
	GetID() string
	GetSymbolCode() string
	GetStrategyName() string
}

// Observation は SniperNest が観測し、整理した「現在の事実」です。
// Sniper はこれを受け取って判断を下します。
type Observation struct {
	Tick               tick.Tick
	Positions          []position.Position
	Performance        Performance
	ActiveOrders       []*order.Order
	HasProcessingTrade bool
	BlockingOrder      *order.Order
}

// HoldQty は現在の事実上の保有数量を返します
func (o Observation) HoldQty() float64 {
	var total float64
	for _, p := range o.Positions {
		if p.Action == order.ACTION_SELL {
			total -= p.LeavesQty
		} else {
			total += p.LeavesQty
		}
	}
	return total
}

// FireAction はスナイパーの意思決定と、実行すべきアクションのペアです。
type FireAction struct {
	SniperID string
	Bullet   Bullet
}

// SniperNest は特定の銘柄（Symbol）におけるスナイパーたちを束ねるドメイン集約（Aggregate Root）です。
// 子コンポーネント（OrderTracker, PositionTracker, PerformanceTracker, CooldownTracker）をオーケストレートし、銘柄ごとの取引状態を保護します。
type SniperNest struct {
	SymbolCode   string
	Detail       symbol.Symbol // 銘柄情報
	snipers      []*Sniper
	orders       *OrderTracker
	positions    *PositionTracker
	performance  *PerformanceTracker
	cooldowns    *CooldownTracker
	Logger       *slog.Logger
	mu           sync.Mutex
	lastTickTime time.Time // 🌟 最新のシミュレーション時刻を保存（エラー発生時の時間軸統一用）
}

func NewSniperNest(code string, detail symbol.Symbol, snipers []*Sniper, logger *slog.Logger) *SniperNest {
	if logger == nil {
		logger = slog.Default()
	}
	return &SniperNest{
		SymbolCode:  code,
		Detail:      detail,
		snipers:     snipers,
		orders:      NewOrderTracker(logger),
		positions:   NewPositionTracker(logger),
		performance: NewPerformanceTracker(),
		cooldowns:   NewCooldownTracker(),
		Logger:      logger,
	}
}

// GetSymbolCodes は対象の全銘柄コードのリストを返します。
func (n *SniperNest) GetSymbolCodes() []string {
	return []string{n.SymbolCode}
}

// HandleTick は時価（Tick）の更新を受け取り、配下の各スナイパーに Observation を配分して意思決定を促します。
// アクション（発注・キャンセル）が必要な場合は FireAction を生成して返します。
func (n *SniperNest) HandleTick(t tick.Tick) []FireAction {
	var actions []FireAction
	for _, s := range n.snipers {
		if s.GetLifecycle() == LifecycleStopped {
			continue
		}
		obs := n.PrepareObservation(s.ID, t, s.ExecutionPolicy)

		input := strategy.StrategyInput{
			Position:   obs.CalculateVirtualPosition(),
			LatestTick: obs.Tick,
		}

		target := s.Evaluate(input)
		bullet := n.ReconcileTarget(s.ID, obs.Tick, target, s.Exchange, s.MarginTradeType, s.AccountType, s.ExecutionPolicy)

		if bullet != nil {
			actions = append(actions, FireAction{
				SniperID: s.ID,
				Bullet:   bullet,
			})
			// 新規発注なら、自身の管理リストに注文を追加して追跡を開始する
			if ordBullet, ok := bullet.(OrderBullet); ok {
				n.AddOrder(s.ID, ordBullet.Order)
			}
		}
	}
	return actions
}

// ForceExit は配下の全スナイパーに緊急撤退を命じます。
func (n *SniperNest) ForceExit() {
	for _, s := range n.snipers {
		s.ForceExit()
	}
}

// GetSymbolCode は対象の銘柄コードを返します。
func (n *SniperNest) GetSymbolCode() string {
	return n.SymbolCode
}

// GetActiveOrders は配下の全スナイパーが追跡中の未完了注文を集約して返します。
func (n *SniperNest) GetActiveOrders() []*order.Order {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.orders.GetAllActive()
}

// UpdateOrders は注文・約定レポートをもとに、内部の状態を更新します。
func (n *SniperNest) UpdateOrders(report order.Orders) {
	n.Update(report, time.Now())
}

// GetPerformance は指定したスナイパーの成績を取得します。
func (n *SniperNest) GetPerformance(sniperID string) Performance {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.performance.Get(sniperID)
}

// GetUnrealizedPnL は指定したスナイパーの含み損益を計算します。
func (n *SniperNest) GetUnrealizedPnL(sniperID string, currentPrice float64) float64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.positions.GetUnrealizedPnL(sniperID, currentPrice)
}

// GetExchanges はこのネスト内のスナイパーが動作する一意な取引所のリストを返します。
func (n *SniperNest) GetExchanges() []order.ExchangeMarket {
	seen := make(map[order.ExchangeMarket]bool)
	var list []order.ExchangeMarket
	for _, s := range n.snipers {
		if !seen[s.Exchange] {
			seen[s.Exchange] = true
			list = append(list, s.Exchange)
		}
	}
	return list
}

// GetReportableTargets は内部のスナイパー群を ReportableTarget インターフェースの型にキャストして返します。
func (n *SniperNest) GetReportableTargets() []ReportableTarget {
	var targets []ReportableTarget
	for _, s := range n.snipers {
		targets = append(targets, s)
	}
	return targets
}

// HasSniper は指定したスナイパーIDがこのネスト内に存在するか判定します。
func (n *SniperNest) HasSniper(sniperID string) bool {
	for _, s := range n.snipers {
		if s.ID == sniperID {
			return true
		}
	}
	return false
}

// FailSendingOrder は対象のスナイパーに発注失敗を通知します。
func (n *SniperNest) FailSendingOrder(sniperID string, ord *order.Order) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.orders.FailOrder(sniperID, ord) {
		if ord.CashMargin == order.CASH_MARGIN_MARGIN_EXIT {
			errTime := n.lastTickTime
			if errTime.IsZero() {
				errTime = time.Now()
			}
			n.cooldowns.TriggerWithTime(sniperID, errTime)
		}
	}
}

// UpdateOrderID は対象のスナイパーが持つ注文IDを最新に更新します。
func (n *SniperNest) UpdateOrderID(sniperID string, ord *order.Order, newID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.orders.UpdateOrderID(sniperID, ord, newID)
}

// RevertOrderStatus は注文ステータスを強制的にロールバックします（ゾンビ修復用）
func (n *SniperNest) RevertOrderStatus(sniperID string, ord *order.Order, status order.OrderStatus) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.orders.RevertOrderStatus(sniperID, ord, status)
}

// AddOrder は新規注文を追跡対象に追加します。
func (n *SniperNest) AddOrder(sniperID string, ord *order.Order) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.orders.Add(sniperID, ord)
}

// GetSniperActiveOrders は特定のスナイパーのアクティブな注文リストを返します。
func (n *SniperNest) GetSniperActiveOrders(sniperID string) []*order.Order {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.orders.GetActive(sniperID)
}

// HoldQty は特定のスナイパーの物理保有数量を返します。
func (n *SniperNest) HoldQty(sniperID string) float64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.positions.HoldQty(sniperID)
}

func reportContainsID(report order.Orders, id string) bool {
	for _, ext := range report.Orders {
		if ext.ID == id {
			return true
		}
	}
	return false
}

func (n *SniperNest) applyExecution(sniperID string, exec order.Execution, action order.Action, parentOrder *order.Order) {
	if n.orders.IsExecutionProcessed(exec.ID) {
		return
	}
	n.orders.MarkExecutionProcessed(exec.ID)

	n.positions.ApplyExecution(sniperID, n.Detail.Code, exec, action, parentOrder, func(pnl float64) {
		n.performance.RecordPnL(sniperID, pnl)
	})
}

// Update は API からの注文レポートを受け取り、内部の「事実」を更新します。
func (n *SniperNest) Update(report order.Orders, now time.Time) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.orders.Update(report, n.Detail, now, func(sniperID string, exec order.Execution, action order.Action, orderCreatedAt time.Time, parentOrder *order.Order) {
		n.applyExecution(sniperID, exec, action, parentOrder)
	})
}

// PrepareObservation は最新の Tick をもとに、指定した Sniper に渡すためのスナップショットを作成します。
func (n *SniperNest) PrepareObservation(sniperID string, t tick.Tick, policy strategy.ExecutionPolicy) Observation {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.lastTickTime = t.CurrentPriceTime // 🌟 最新のシミュレーション時刻を保存

	activeOrders, hasProcessingTrade, blockingOrder := n.orders.PrepareActiveOrders(sniperID, t, policy)
	posCopy := n.positions.GetCopy(sniperID)

	return Observation{
		Tick:               t,
		Positions:          posCopy,
		Performance:        n.performance.Get(sniperID),
		ActiveOrders:       activeOrders,
		HasProcessingTrade: hasProcessingTrade,
		BlockingOrder:      blockingOrder,
	}
}

func (n *SniperNest) ReconcileTarget(
	sniperID string,
	t tick.Tick,
	target strategy.TargetPosition,
	exchange order.ExchangeMarket,
	marginType order.MarginTradeType,
	accountType order.AccountType,
	policy strategy.ExecutionPolicy,
) Bullet {
	n.mu.Lock()
	defer n.mu.Unlock()

	now := t.CurrentPriceTime
	if now.IsZero() {
		now = time.Now()
	}

	if target.Price > 0 {
		target.Price = n.Detail.RoundPrice(target.Price)
	}
	if target.HasIfDone && target.ExitPrice > 0 {
		target.ExitPrice = n.Detail.RoundPrice(target.ExitPrice)
	}

	// --- 1. インフライト注文の分類と集計 ---
	stats := n.orders.GetInflightStats(sniperID)

	// キャンセル送信中の注文がある場合は、その確定を待つため新規発注をブロック
	if len(stats.CancelingOrders) > 0 {
		return nil
	}

	// 仮想ポジションおよび物理ポジションの算出

	activeOrders := n.orders.GetActive(sniperID)
	positions := n.positions.GetCopy(sniperID)
	
	var virtualQty float64
	for _, p := range positions {
		if p.Action == order.ACTION_SELL {
			virtualQty -= p.LeavesQty
		} else {
			virtualQty += p.LeavesQty
		}
	}
	for _, curr := range activeOrders {
		if curr != nil && curr.IsFillExpected() {
			if curr.CashMargin == order.CASH_MARGIN_MARGIN_ENTRY {
				switch curr.Action {
				case order.ACTION_BUY:
					virtualQty += curr.OrderQty
				case order.ACTION_SELL:
					virtualQty -= curr.OrderQty
				}
			}
		}
	}

	// ポジション反転の安全弁
	effectiveTargetQty := target.Qty
	if virtualQty > 0 && target.Qty < 0 {
		effectiveTargetQty = 0
	} else if virtualQty < 0 && target.Qty > 0 {
		effectiveTargetQty = 0
	}

	// --- 2. 矛盾注文のキャンセル処理 ---
	for _, o := range stats.ActiveOrders {
		if o == nil || !o.CanCancel() {
			continue
		}
		shouldCancel := false

		// もし戦略が自前のキャンセルロジック（CancelChecker）を持っているなら、それを最優先する
		var s *Sniper
		for _, sniper := range n.snipers {
			if sniper.ID == sniperID {
				s = sniper
				break
			}
		}
		if s != nil {
			if checker, isChecker := s.Strategy.(strategy.CancelChecker); isChecker {
				var avgPrice float64
				var totalCost float64
				for _, p := range positions {
					if p.Action == order.ACTION_SELL {
						totalCost -= p.Price * p.LeavesQty
					} else {
						totalCost += p.Price * p.LeavesQty
					}
				}
				if virtualQty > 0 {
					avgPrice = totalCost / virtualQty
				}
				shouldCancel = checker.ShouldCancel(strategy.StrategyInput{
					Position: strategy.Position{
						Qty:          virtualQty,
						AveragePrice: avgPrice,
					},
					LatestTick: t,
				}, o)
				goto afterCheck
			}
		}

		if effectiveTargetQty > 0 {
			if (o.Action == order.ACTION_SELL && o.CashMargin == order.CASH_MARGIN_MARGIN_ENTRY) ||
				(o.Action == order.ACTION_BUY && o.CashMargin == order.CASH_MARGIN_MARGIN_EXIT) {
				shouldCancel = true
			}
		} else if effectiveTargetQty < 0 {
			if (o.Action == order.ACTION_BUY && o.CashMargin == order.CASH_MARGIN_MARGIN_ENTRY) ||
				(o.Action == order.ACTION_SELL && o.CashMargin == order.CASH_MARGIN_MARGIN_EXIT) {
				shouldCancel = true
			}
		} else {
			if o.CashMargin == order.CASH_MARGIN_MARGIN_ENTRY {
				if target.Price == 0 {
					shouldCancel = true
				}
			}
		}

	afterCheck:
		if shouldCancel {
			fmt.Printf("🔄 [%s] 目標ポジション変更により矛盾注文(%s)をキャンセルします [Status:%v, Target:%f]\n",
				n.Detail.Code, o.ID, o.Status(), effectiveTargetQty)
			if o.InternalState() != order.STATE_PREPARING {
				o.ToCancelSent()
				o.CancelSentAt = now
			}
			return CancelBullet{OrderID: o.ID}
		}
	}

	// --- 3. ギャップ計算と新規発注判定 ---
	var gap float64
	var action order.Action
	var cashMargin order.CashMarginType

	if effectiveTargetQty > 0 {
		gap = effectiveTargetQty - (virtualQty + stats.InflightBuyEntry)
		action = order.ACTION_BUY
		cashMargin = order.CASH_MARGIN_MARGIN_ENTRY
	} else if effectiveTargetQty < 0 {
		gap = effectiveTargetQty - (virtualQty - stats.InflightSellEntry)
		action = order.ACTION_SELL
		cashMargin = order.CASH_MARGIN_MARGIN_ENTRY
	} else {
		if virtualQty > 0 {
			gap = virtualQty - stats.InflightSellExit
			action = order.ACTION_SELL
			cashMargin = order.CASH_MARGIN_MARGIN_EXIT
		} else if virtualQty < 0 {
			gap = math.Abs(virtualQty) - stats.InflightBuyExit
			action = order.ACTION_BUY
			cashMargin = order.CASH_MARGIN_MARGIN_EXIT
		}
	}

	absGap := math.Abs(gap)

	// 同方向かつ同口座区分の進行中注文があるか確認
	var matchingOrder *order.Order
	for _, o := range stats.ActiveOrders {
		if o == nil {
			continue
		}
		if o.Action == action && o.CashMargin == cashMargin {
			if o.InternalState() == order.STATE_PREPARING {
				matchingOrder = o
				break
			}
			matchingOrder = o
		}
	}

	// 期待される注文内容を定義
	var desiredQty float64
	var desiredPrice float64
	var desiredOrderType order.OrderType
	var desiredReason string

	if cashMargin == order.CASH_MARGIN_MARGIN_EXIT {
		desiredQty = math.Abs(virtualQty)
		if target.HasIfDone {
			desiredPrice = target.ExitPrice
			desiredOrderType = target.ExitOrderType
			desiredReason = target.ExitReason
		} else {
			desiredPrice = target.Price
			desiredOrderType = target.OrderType
			desiredReason = target.Reason
		}
	} else {
		desiredQty = math.Abs(effectiveTargetQty)
		desiredPrice = target.Price
		desiredOrderType = target.OrderType
		desiredReason = target.Reason
		// 既存のエントリー注文があり、かつターゲット価格が 0 (HOLDなど) の場合は、
		// 既存注文の価格とタイプを引き継ぐことで、不要なキャンセルを防ぐ。
		if matchingOrder != nil && matchingOrder.CashMargin == order.CASH_MARGIN_MARGIN_ENTRY && target.Price == 0 {
			desiredPrice = matchingOrder.OrderPrice
			desiredOrderType = matchingOrder.Type
		}
	}

	var desiredTradeType brain.TradeType
	if cashMargin == order.CASH_MARGIN_MARGIN_EXIT {
		desiredTradeType = brain.TradeExit
	} else {
		desiredTradeType = brain.TradeEntry
	}

	desiredSignal := brain.Signal{
		Action:    brain.Action(action),
		TradeType: desiredTradeType,
		Quantity:  desiredQty,
		Price:     desiredPrice,
		OrderType: desiredOrderType,
		Reason:    desiredReason,
	}

	// ギャップが極小で、かつ既存注文の更新も必要ない場合は早期リターン
	if absGap < 1.0 {
		if matchingOrder == nil {
			return nil
		}

		isIdentical := matchingOrder.Action == action &&
			matchingOrder.OrderQty == desiredSignal.Quantity &&
			(matchingOrder.OrderPrice == desiredSignal.Price || (math.IsNaN(matchingOrder.OrderPrice) && math.IsNaN(desiredSignal.Price))) &&
			matchingOrder.CashMargin == cashMargin

		if isIdentical || policy.IsOrderDesired(matchingOrder, desiredSignal, n.Detail) {
			return nil
		}
	}

	// 目標価格が NaN の場合は、新規発注や既存注文の上書きを行わない（ガードレール）
	if math.IsNaN(desiredSignal.Price) {
		return nil
	}

	// 返済エラー時のクールダウン
	isExit := (cashMargin == order.CASH_MARGIN_MARGIN_EXIT)
	if isExit && n.cooldowns.IsCoolingDown(sniperID, now) {
		n.Logger.Warn("⏳ 前回の返済エラーから1秒未満のため、返済注文の発注を一時見合わせます（建玉反映待ち）",
			slog.String("symbol", n.Detail.Code),
		)
		return nil
	}

	if matchingOrder != nil {
		isIdentical := matchingOrder.Action == action &&
			matchingOrder.OrderQty == desiredSignal.Quantity &&
			(matchingOrder.OrderPrice == desiredSignal.Price || (math.IsNaN(matchingOrder.OrderPrice) && math.IsNaN(desiredSignal.Price))) &&
			matchingOrder.CashMargin == cashMargin

		if isIdentical || policy.IsOrderDesired(matchingOrder, desiredSignal, n.Detail) {
			return nil
		}

		fmt.Printf("🔄 [%s] 目標値変更により、既存注文(%s)を上書きします [Status:%v, OldQty:%f, NewQty:%f, OldPrice:%f, NewPrice:%f]\n",
			n.Detail.Code, matchingOrder.ID, matchingOrder.Status(),
			matchingOrder.OrderQty, desiredQty, matchingOrder.OrderPrice, desiredPrice)

		if !matchingOrder.CanCancel() {
			// API送信中やキャンセル送信中のため、安全のため完了するまで上書きを保留する
			return nil
		}

		if matchingOrder.InternalState() == order.STATE_PREPARING {
			// 送信前の場合は、キャンセル要求を送らずに新規上書き注文の発行へ進む
		} else {
			matchingOrder.ToCancelSent()
			matchingOrder.CancelSentAt = now
			return CancelBullet{OrderID: matchingOrder.ID}
		}
	}
	lockedHoldIDs := order.ActiveOrders(activeOrders).LockedHoldIDs()

	entry, exit := n.buildOrderPairFromTarget(sniperID, target, action, absGap, cashMargin, exchange, marginType, accountType, lockedHoldIDs)
	if exit != nil {
		entry.IfDone = exit
	}

	entry.CreatedAt = now

	return OrderBullet{Order: entry}
}

func (n *SniperNest) buildOrderPairFromTarget(
	sniperID string,
	target strategy.TargetPosition,
	action order.Action,
	qty float64,
	cashMargin order.CashMarginType,
	exchange order.ExchangeMarket,
	marginType order.MarginTradeType,
	accountType order.AccountType,
	lockedHoldIDs map[string]bool,
) (*order.Order, *order.Order) {
	isExit := (cashMargin == order.CASH_MARGIN_MARGIN_EXIT)

	var closePositions []order.ClosePosition
	if isExit {
		closePositions, _ = n.positions.MatchPositionsToClose(sniperID, action, qty, lockedHoldIDs)
	}

	entryReq := &order.OrderRequest{
		Exchange:        exchange,
		SecurityType:    order.SECURITY_TYPE_STOCK,
		MarginTradeType: marginType,
		AccountType:     accountType,
	}
	if isExit {
		entryReq.ClosePositions = closePositions
		if len(closePositions) == 0 {
			entryReq.ClosePositionOrder = order.CLOSE_POSITION_ASC_DAY_DEC_PL
		}
	}

	entry := order.NewOrder(
		order.GenerateLocalID(),
		n.Detail.Code,
		action,
		target.Price,
		qty,
		order.WithType(target.OrderType),
		order.WithCashMargin(cashMargin),
		order.WithRequest(entryReq),
		order.WithReason(target.Reason),
	)
	entry.ToPending()

	var exit *order.Order
	if target.HasIfDone {
		var exitAction order.Action
		if action == order.ACTION_BUY {
			exitAction = order.ACTION_SELL
		} else {
			exitAction = order.ACTION_BUY
		}

		exitReq := &order.OrderRequest{
			Exchange:           exchange,
			SecurityType:       order.SECURITY_TYPE_STOCK,
			MarginTradeType:    marginType,
			AccountType:        accountType,
			ClosePositionOrder: order.CLOSE_POSITION_ASC_DAY_DEC_PL,
		}

		exit = order.NewOrder(
			order.GenerateLocalID(),
			n.Detail.Code,
			exitAction,
			target.ExitPrice,
			qty,
			order.WithType(target.ExitOrderType),
			order.WithCashMargin(order.CASH_MARGIN_MARGIN_EXIT),
			order.WithRequest(exitReq),
			order.WithReason(target.ExitReason),
		)
	}

	return entry, exit
}

// CalculateVirtualPosition は Observation の状態から約定予定分を含んだ仮想ポジションを計算します
func (obs Observation) CalculateVirtualPosition() strategy.Position {
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
			// 🌟 エントリー注文（新規建て）の約定予定のみを仮想ポジションに加算する。
			// 決済注文（返済）の約定予定は、物理ポジションから減算しない（決済完了までポジション維持として扱う）。
			if curr.CashMargin == order.CASH_MARGIN_MARGIN_ENTRY {
				switch curr.Action {
				case order.ACTION_BUY:
					totalQty += curr.OrderQty
					totalCost += curr.OrderPrice * curr.OrderQty
				case order.ACTION_SELL:
					totalQty -= curr.OrderQty
				}
			}
		}
	}
	avgPrice := 0.0
	if totalQty > 0 {
		avgPrice = totalCost / totalQty
	}
	return strategy.Position{Qty: totalQty, AveragePrice: avgPrice}
}
