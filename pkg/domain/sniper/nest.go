package sniper

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
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
	Tick         tick.Tick
	Positions    position.Positions
	ActiveOrders []*order.Order
}

// FireAction はスナイパーの意思決定と、実行すべきアクションのペアです。
type FireAction struct {
	SniperID string
	Bullet   Bullet
}

// ReconciliationMode はネスト配下の戦略群の目標ポジション調停モードを表します
type ReconciliationMode string

const (
	// ReconciliationModeLegacy は従来仕様（個別評価・クロス取引抑止ガード）です
	ReconciliationModeLegacy ReconciliationMode = "legacy"
	// ReconciliationModeAggregate は真のターゲットポジション集約（相殺・ドテン自動執行）です
	ReconciliationModeAggregate ReconciliationMode = "aggregated"
)

// SniperNest は特定の銘柄（Symbol）におけるスナイパーたちを束ねるドメイン集約（Aggregate Root）です。
// 子コンポーネント（OrderTracker, PositionTracker, PerformanceTracker, CooldownTracker）をオーケストレートし、銘柄ごとの取引状態を保護します。
type SniperNest struct {
	SymbolCode         string
	Detail             symbol.Symbol // 銘柄情報
	snipers            []*Sniper
	orders             *OrderTracker
	positions          *PositionTracker
	performance        *PerformanceTracker
	cooldowns          *CooldownTracker
	Logger             *slog.Logger
	mu                 sync.Mutex
	lastTickTime       time.Time          // 🌟 最新のシミュレーション時刻を保存（エラー発生時の時間軸統一用）
	reconciliationMode ReconciliationMode // 🌟 調停モード（デフォルト: legacy）
}

// SetReconciliationMode は調停モードを設定します
func (n *SniperNest) SetReconciliationMode(mode ReconciliationMode) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.reconciliationMode = mode
}

// GetReconciliationMode は現在の調停モードを返します
func (n *SniperNest) GetReconciliationMode() ReconciliationMode {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.reconciliationMode == "" {
		return ReconciliationModeLegacy
	}
	return n.reconciliationMode
}

func NewSniperNest(code string, detail symbol.Symbol, snipers []*Sniper, logger *slog.Logger) *SniperNest {
	if logger == nil {
		logger = slog.Default()
	}
	positions := NewPositionTracker(logger)
	return &SniperNest{
		SymbolCode:  code,
		Detail:      detail,
		snipers:     snipers,
		orders:      NewOrderTracker(positions, logger),
		positions:   positions,
		performance:        NewPerformanceTracker(),
		cooldowns:          NewCooldownTracker(),
		Logger:             logger,
		reconciliationMode: ReconciliationModeLegacy,
	}
}

// GetSymbolCodes は対象の全銘柄コードのリストを返します。
func (n *SniperNest) GetSymbolCodes() []string {
	return []string{n.SymbolCode}
}

// HandleTick は時価（Tick）の更新を受け取り、設定された調停モード（Legacy / Aggregated）に応じて
// 配下の各スナイパーの意思決定とアクション生成を行います。
func (n *SniperNest) HandleTick(t tick.Tick) []FireAction {
	if n.GetReconciliationMode() == ReconciliationModeAggregate {
		return n.handleTickAggregated(t)
	}
	return n.handleTickLegacy(t)
}

// handleTickLegacy は従来の個別評価・クロス取引抑止ガードによる発注処理です。
func (n *SniperNest) handleTickLegacy(t tick.Tick) []FireAction {
	var actions []FireAction
	for _, s := range n.snipers {
		if s.GetLifecycle() == LifecycleStopped {
			continue
		}
		policy := s.ExecutionPolicy
		if policy != nil {
			activeOrders := n.orders.GetActive(s.ID)
			for _, curr := range activeOrders {
				if !curr.IsEligibleForSyntheticFill() {
					continue
				}

				policy.UpdateState(curr, t)

				if policy.ShouldReset(curr, t) {
					curr.ResetSynthetic()
					fmt.Printf("🔄 [%s] 価格が離れたため疑似約定ステータスをリセットしました: %s\n", curr.Symbol, curr.ID)
					continue
				}

				if curr.IsFillExpected() {
					if policy.ShouldTimeout(curr, t) {
						curr.MarkAsTimedOut()
						fmt.Printf("💔 [%s] 疑似約定がタイムアウトしました: %s\n", curr.Symbol, curr.ID)
					}
					continue
				}

				if policy.ShouldFill(curr, t) {
					curr.MarkAsFillExpected(t.CurrentPriceTime)
					fmt.Printf("⚡ [%s] 疑似約定を検知しました: %s (Price: %f, Tick: %f)\n", curr.Symbol, curr.ID, curr.OrderPrice, t.Price)
				}
			}
		}
		obs := n.PrepareObservation(s.ID, t)
		virtualPos := s.CalculateVirtualPosition(obs.Positions, obs.ActiveOrders)

		input := strategy.StrategyInput{
			Position:   virtualPos,
			LatestTick: obs.Tick,
		}

		target := s.Evaluate(input)
		bullet := n.ReconcileTarget(s.ID, obs.Tick, target, s.Exchange, s.MarginTradeType, s.AccountType, s.ExecutionPolicy)

		if bullet != nil {
			// 🌟 不公正取引（自己対当クロス）の自動調停ロジック
			if ordBullet, ok := bullet.(OrderBullet); ok {
				newOrd := ordBullet.Order

				var conflictingOrder *order.Order
				var conflictingSniperID string

				// 他のスナイパーが持つ「売買方向が反対」の未約定注文を探す
				for otherSniperID, otherOrders := range n.orders.activeOrders {
					if otherSniperID == s.ID {
						continue
					}
					for _, actOrd := range otherOrders {
						if actOrd.Action != newOrd.Action {
							conflictingOrder = actOrd
							conflictingSniperID = otherSniperID
							break
						}
					}
					if conflictingOrder != nil {
						break
					}
				}

				if conflictingOrder != nil {
					if newOrd.CashMargin == order.CASH_MARGIN_MARGIN_EXIT {
						// 返済（決済）注文を優先するため、競合する反対側の新規指値注文などをキャンセルする
						n.Logger.Warn("⚠️ [CrossTradeRegulation] 返済注文を優先するため、競合する反対側の未約定注文を自動キャンセルします",
							slog.String("symbol", n.SymbolCode),
							slog.String("exit_sniper", s.ID),
							slog.String("canceling_order_id", conflictingOrder.ID),
							slog.String("canceling_sniper", conflictingSniperID))

						// キャンセル命令を発行して送信リストに追加
						actions = append(actions, FireAction{
							SniperID: conflictingSniperID,
							Bullet:   CancelBullet{OrderID: conflictingOrder.ID},
						})
					} else {
						// 新規エントリー注文の場合は、単に発注をスキップして様子見する
						n.Logger.Warn("⚠️ [CrossTradeRegulation] 不公正取引（自己対当クロス）を防止するため、新規エントリーを一時的に抑止します",
							slog.String("symbol", n.SymbolCode),
							slog.String("entry_sniper", s.ID),
							slog.String("conflicting_order_id", conflictingOrder.ID),
							slog.String("conflicting_sniper", conflictingSniperID))
					}

					// 今回の発注は一旦スキップし、次のループに回す
					continue
				}
			}

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

// handleTickAggregated は真のターゲットポジション集約（相殺・ドテン自動執行）ロジックです。
// 全スナイパーの希望数量を集約し、反対建玉が存在する場合は即座に相殺決済を行います。
func (n *SniperNest) handleTickAggregated(t tick.Tick) []FireAction {
	type sniperEval struct {
		sniper *Sniper
		target strategy.TargetPosition
		obs    Observation
	}
	var evals []sniperEval
	totalTargetQty := 0.0

	var longSniper, shortSniper *Sniper
	var longTarget, shortTarget strategy.TargetPosition
	var longObs, shortObs Observation

	for _, s := range n.snipers {
		if s.GetLifecycle() == LifecycleStopped {
			continue
		}

		policy := s.ExecutionPolicy
		if policy != nil {
			activeOrders := n.orders.GetActive(s.ID)
			for _, curr := range activeOrders {
				if !curr.IsEligibleForSyntheticFill() {
					continue
				}

				policy.UpdateState(curr, t)

				if policy.ShouldReset(curr, t) {
					curr.ResetSynthetic()
					fmt.Printf("🔄 [%s] 価格が離れたため疑似約定ステータスをリセットしました: %s\n", curr.Symbol, curr.ID)
					continue
				}

				if curr.IsFillExpected() {
					if policy.ShouldTimeout(curr, t) {
						curr.MarkAsTimedOut()
						fmt.Printf("💔 [%s] 疑似約定がタイムアウトしました: %s\n", curr.Symbol, curr.ID)
					}
					continue
				}

				if policy.ShouldFill(curr, t) {
					curr.MarkAsFillExpected(t.CurrentPriceTime)
					fmt.Printf("⚡ [%s] 疑似約定を検知しました: %s (Price: %f, Tick: %f)\n", curr.Symbol, curr.ID, curr.OrderPrice, t.Price)
				}
			}
		}

		obs := n.PrepareObservation(s.ID, t)
		virtualPos := s.CalculateVirtualPosition(obs.Positions, obs.ActiveOrders)

		input := strategy.StrategyInput{
			Position:   virtualPos,
			LatestTick: obs.Tick,
		}

		target := s.Evaluate(input)
		evals = append(evals, sniperEval{
			sniper: s,
			target: target,
			obs:    obs,
		})
		totalTargetQty += target.Qty

		if target.IsLong() || strings.Contains(s.ID, "long") {
			longSniper = s
			longTarget = target
			longObs = obs
		}
		if target.IsShort() || strings.Contains(s.ID, "short") {
			shortSniper = s
			shortTarget = target
			shortObs = obs
		}
	}

	// 物理確定建玉
	physicalHoldQty := n.positions.HoldQty()

	// --- 内部ヘルパー関数群（可読性と保守性の向上） ---
	fireTarget := func(s *Sniper, obs Observation, target strategy.TargetPosition) []FireAction {
		var acts []FireAction
		bullet := n.ReconcileTarget(s.ID, obs.Tick, target, s.Exchange, s.MarginTradeType, s.AccountType, s.ExecutionPolicy)
		if bullet != nil {
			acts = append(acts, FireAction{SniperID: s.ID, Bullet: bullet})
			if ordBullet, ok := bullet.(OrderBullet); ok {
				n.AddOrder(s.ID, ordBullet.Order)
			}
		}
		return acts
	}

	cancelActiveEntries := func(sniperID string) []FireAction {
		var acts []FireAction
		stats := n.orders.GetInflightStats(sniperID)
		for _, o := range stats.ActiveOrders {
			if o != nil && o.CanCancel() && o.CashMargin == order.CASH_MARGIN_MARGIN_ENTRY {
				o.ToCancelSent()
				o.CancelSentAt = t.CurrentPriceTime
				acts = append(acts, FireAction{
					SniperID: sniperID,
					Bullet:   CancelBullet{OrderID: o.ID},
				})
			}
		}
		return acts
	}

	var actions []FireAction

	// 🌟 全スナイパーの集約目標 (totalTargetQty) に基づく調停
	switch {
	case totalTargetQty > 0:
		// 【ロング優勢】ショート建玉があればまずドテン相殺決済（建玉解消までロング発注は待機）
		if physicalHoldQty < 0 && shortSniper != nil {
			return fireTarget(shortSniper, shortObs, strategy.TargetPosition{
				Qty:       0,
				Price:     0,
				OrderType: order.ORDER_TYPE_MARKET,
				Reason:    "NET_OFFSET_EXIT_SHORT",
			})
		}
		// ショート側の未約定新規売り注文をキャンセル
		if shortSniper != nil {
			actions = append(actions, cancelActiveEntries(shortSniper.ID)...)
		}
		// ロング側スナイパーに集約目標を発注・維持
		if longSniper != nil {
			target := longTarget
			target.Qty = totalTargetQty
			actions = append(actions, fireTarget(longSniper, longObs, target)...)
		}

	case totalTargetQty < 0:
		// 【ショート優勢】ロング建玉があればまずドテン相殺決済（建玉解消までショート発注は待機）
		if physicalHoldQty > 0 && longSniper != nil {
			return fireTarget(longSniper, longObs, strategy.TargetPosition{
				Qty:       0,
				Price:     0,
				OrderType: order.ORDER_TYPE_MARKET,
				Reason:    "NET_OFFSET_EXIT_LONG",
			})
		}
		// ロング側の未約定新規買い注文をキャンセル
		if longSniper != nil {
			actions = append(actions, cancelActiveEntries(longSniper.ID)...)
		}
		// ショート側スナイパーに集約目標を発注・維持
		if shortSniper != nil {
			target := shortTarget
			target.Qty = totalTargetQty
			actions = append(actions, fireTarget(shortSniper, shortObs, target)...)
		}

	default:
		// 【中立・手仕舞い（Target = 0）】建玉があれば全決済、未約定エントリーは全キャンセル
		if physicalHoldQty > 0 && longSniper != nil {
			actions = append(actions, fireTarget(longSniper, longObs, strategy.TargetPosition{
				Qty:       0,
				Price:     0,
				OrderType: order.ORDER_TYPE_MARKET,
				Reason:    "NET_OFFSET_FLAT_LONG",
			})...)
		} else if physicalHoldQty < 0 && shortSniper != nil {
			actions = append(actions, fireTarget(shortSniper, shortObs, strategy.TargetPosition{
				Qty:       0,
				Price:     0,
				OrderType: order.ORDER_TYPE_MARKET,
				Reason:    "NET_OFFSET_FLAT_SHORT",
			})...)
		}
		for _, ev := range evals {
			actions = append(actions, cancelActiveEntries(ev.sniper.ID)...)
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

// ReconcilePositions は取引所から直接取得した実建玉一覧をもとに、メモリ上の建玉状態を自己修復・同期します。
func (n *SniperNest) ReconcilePositions(actualPositions []position.Position) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.positions.Reconcile(n.SymbolCode, actualPositions)
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
	return n.positions.GetUnrealizedPnL(currentPrice)
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

// DestroySendingOrder は対象のスナイパーから注文を即座に完全抹消します（確定失敗用）
func (n *SniperNest) DestroySendingOrder(sniperID string, ord *order.Order) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.orders.DestroyOrder(sniperID, ord)
}

// HandleOrderRejection は発注が取引所で拒絶された際のクリーンアップを行います
func (n *SniperNest) HandleOrderRejection(sniperID string, ord *order.Order, err error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.orders.DestroyOrder(sniperID, ord)

	// 建玉強制削除は、「決済指定内容に誤りがある（取引所にその建玉IDが実在しない）エラー」の時のみ行う。
	var rejectErr order.RejectError
	if ord.CashMargin == order.CASH_MARGIN_MARGIN_EXIT && ord.Request != nil && len(ord.Request.ClosePositions) > 0 {
		if errors.As(err, &rejectErr) && rejectErr.IsPositionMissing() {
			for _, cp := range ord.Request.ClosePositions {
				n.positions.RemovePosition(cp.HoldID)
			}
		}
	}
}

// UpdateOrderID は対象のスナイパーが持つ注文IDを最新に更新します。
func (n *SniperNest) UpdateOrderID(sniperID string, ord *order.Order, newID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.positions.UpdateLockOrderID(ord.ID, newID)
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
	if ord.CashMargin == order.CASH_MARGIN_MARGIN_EXIT && ord.Request != nil {
		_ = n.positions.LockPositions(ord.ID, ord.Request.ClosePositions)
	}
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
	return n.positions.HoldQty()
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
func (n *SniperNest) PrepareObservation(sniperID string, t tick.Tick) Observation {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.lastTickTime = t.CurrentPriceTime // 🌟 最新 of simulation time

	activeOrders := n.orders.PrepareActiveOrders(sniperID)
	posCopy := n.positions.GetCopy()

	return Observation{
		Tick:         t,
		Positions:    posCopy,
		ActiveOrders: activeOrders,
	}
}

// GetCommittedPosition は物理確定建玉とアクティブ注文から、市場に既にコミットしている執行中・約定済みポジションを計算して返します
func (n *SniperNest) GetCommittedPosition(sniperID string) CommittedPosition {
	physicalQty := n.positions.HoldQty()
	orderQty := n.orders.SignedQty(sniperID)

	return CommittedPosition{
		TotalQty: physicalQty + orderQty,
	}
}

// GetTotalCommittedPosition は物理確定建玉とネスト全体の全アクティブ注文からコミット中ポジションを計算して返します
func (n *SniperNest) GetTotalCommittedPosition() CommittedPosition {
	physicalQty := n.positions.HoldQty()
	orderQty := n.orders.TotalSignedQty()

	return CommittedPosition{
		TotalQty: physicalQty + orderQty,
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

	// 物理確定建玉と市場コミット中ポジション（CommittedPosition）の取得
	physicalHoldQty := n.positions.HoldQty()
	committedPos := n.GetCommittedPosition(sniperID)
	if n.reconciliationMode == ReconciliationModeAggregate {
		committedPos = n.GetTotalCommittedPosition()
	}

	// ポジション反転の安全弁:
	// 反対ポジションを持つ場合は、両建てを防ぐため一旦全決済（Target=0）を優先する（エンティティの執行ポリシー）
	effectiveTarget := target
	if committedPos.RequiresReversal(target) {
		effectiveTarget = strategy.TargetPosition{Qty: 0}
	}
	effectiveTargetQty := effectiveTarget.Qty

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
				shouldCancel = checker.ShouldCancel(strategy.StrategyInput{
					Position:   strategy.Position{Qty: committedPos.TotalQty},
					LatestTick: t,
				}, o)
				goto afterCheck
			}
		}

		if effectiveTarget.IsLong() {
			if (o.Action == order.ACTION_SELL && o.CashMargin == order.CASH_MARGIN_MARGIN_ENTRY) ||
				(o.Action == order.ACTION_BUY && o.CashMargin == order.CASH_MARGIN_MARGIN_EXIT) {
				shouldCancel = true
			}
		} else if effectiveTarget.IsShort() {
			if (o.Action == order.ACTION_BUY && o.CashMargin == order.CASH_MARGIN_MARGIN_ENTRY) ||
				(o.Action == order.ACTION_SELL && o.CashMargin == order.CASH_MARGIN_MARGIN_EXIT) {
				shouldCancel = true
			}
		} else {
			if o.CashMargin == order.CASH_MARGIN_MARGIN_ENTRY {
				shouldCancel = true
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

	// --- 3. 目標方向に基づくアクション・口座区分の決定と差分計算 ---
	var action order.Action
	var cashMargin order.CashMarginType
	var desiredQty float64

	if effectiveTarget.IsLong() {
		action = order.ACTION_BUY
		desiredQty = effectiveTarget.AbsQty()
		if physicalHoldQty < 0 {
			cashMargin = order.CASH_MARGIN_MARGIN_EXIT
		} else {
			cashMargin = order.CASH_MARGIN_MARGIN_ENTRY
		}
	} else if effectiveTarget.IsShort() {
		action = order.ACTION_SELL
		desiredQty = effectiveTarget.AbsQty()
		if physicalHoldQty > 0 {
			cashMargin = order.CASH_MARGIN_MARGIN_EXIT
		} else {
			cashMargin = order.CASH_MARGIN_MARGIN_ENTRY
		}
	} else {
		// 目標ノーポジ（手仕舞い決済）
		if physicalHoldQty > 0 {
			action = order.ACTION_SELL
			cashMargin = order.CASH_MARGIN_MARGIN_EXIT
			desiredQty = physicalHoldQty
		} else if physicalHoldQty < 0 {
			action = order.ACTION_BUY
			cashMargin = order.CASH_MARGIN_MARGIN_EXIT
			desiredQty = math.Abs(physicalHoldQty)
		} else {
			// 物理建玉も0、目標も0（未約定注文の整理はセクション2で完了済み）
			return nil
		}
	}

	gap := committedPos.GapTo(effectiveTarget)
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
	var desiredPrice float64
	var desiredOrderType order.OrderType
	var desiredReason string

	if cashMargin == order.CASH_MARGIN_MARGIN_EXIT {
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

		matchingOrder.ToCancelSent()
		matchingOrder.CancelSentAt = now
		return CancelBullet{OrderID: matchingOrder.ID}
	}

	// 🌟 新規発注・重複返済の安全弁（Inflight Guard）:
	// 取引所での受付・約定および建玉同期が完了するまで、多重発注や重複返済を厳格に保留・防止する。
	isExitOrder := (cashMargin == order.CASH_MARGIN_MARGIN_EXIT)

	for _, o := range stats.ActiveOrders {
		if o == nil || o.IsCompleted() {
			continue
		}

		if isExitOrder {
			// 【返済注文の発注時】
			// 既に未完了の返済注文が存在する場合のみ、二重返済防止のため保留する。
			// （新規注文のスタックや未約定によって手仕舞い返済が阻害されるのを防ぐ）
			if o.CashMargin == order.CASH_MARGIN_MARGIN_EXIT {
				return nil
			}
		} else {
			// 【新規エントリー注文の発注時】
			// 1. 送信中（PREPARING/PENDING）の注文がある場合は完了まで新規発注を保留
			if o.InternalState() == order.STATE_PREPARING || o.InternalState() == order.STATE_PENDING {
				return nil
			}
			// 2. 約定待ちの成行新規注文がある場合は多重エントリーを防止するため保留
			if o.IsMarket() && o.CashMargin == order.CASH_MARGIN_MARGIN_ENTRY && o.CumQty < o.OrderQty {
				return nil
			}
			// 3. 決済中のポジションがある場合は新規エントリーを保留（両建て・混乱防止）
			if o.CashMargin == order.CASH_MARGIN_MARGIN_EXIT {
				return nil
			}
		}
	}

	orderQty := desiredQty
	if absGap < orderQty {
		orderQty = absGap
	}
	if isExitOrder && desiredQty > 0 && desiredQty < orderQty {
		orderQty = desiredQty
	}

	entry, exit := n.buildOrderPairFromTarget(sniperID, target, action, orderQty, cashMargin, exchange, marginType, accountType)
	if entry == nil {
		return nil
	}
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
) (*order.Order, *order.Order) {
	isExit := (cashMargin == order.CASH_MARGIN_MARGIN_EXIT)

	var closePositions []order.ClosePosition
	if isExit {
		closePositions, _ = n.positions.MatchPositionsToClose(action, qty)
		if len(closePositions) == 0 {
			// 🌟 信用返済時、ロック可能な建玉が1つもない（すべて決済中または残存ゼロ）場合は、
			// 誤って重複返済や空返済を行わないよう発注を中止する。
			return nil, nil
		}
	}

	entryReq := &order.OrderRequest{
		Exchange:        exchange,
		SecurityType:    order.SECURITY_TYPE_STOCK,
		MarginTradeType: marginType,
		AccountType:     accountType,
	}
	if isExit {
		entryReq.ClosePositions = closePositions
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


