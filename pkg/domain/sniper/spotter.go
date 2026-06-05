package sniper

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/position"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

// Observation は Spotter が観測し、整理した「現在の事実」です。
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

type tombstoneEntry struct {
	ord       *order.Order
	deletedAt time.Time
}

// Spotter は特定の銘柄の「現実の状態（事実）」を監視・維持する役割を担います。
type Spotter struct {
	Detail              symbol.Symbol
	sniperPositions     map[string][]position.Position
	sniperPerformance   map[string]Performance
	processedExecutions map[string]bool
	sniperActiveOrders  map[string][]*order.Order
	tombstones          map[string][]tombstoneEntry // 墓標リスト
	Logger              *slog.Logger
	mu                  sync.Mutex
}

func NewSpotter(detail symbol.Symbol, logger *slog.Logger) *Spotter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Spotter{
		Detail:              detail,
		sniperPositions:     make(map[string][]position.Position),
		sniperPerformance:   make(map[string]Performance),
		processedExecutions: make(map[string]bool),
		sniperActiveOrders:  make(map[string][]*order.Order),
		tombstones:          make(map[string][]tombstoneEntry),
		Logger:              logger,
	}
}

func reportContainsID(report order.Orders, id string) bool {
	for _, ext := range report.Orders {
		if ext.ID == id {
			return true
		}
	}
	return false
}

// Update は API からの注文レポートを受け取り、内部の「事実」を更新します。
func (s *Spotter) Update(report order.Orders, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// ボット全体（すべてのスナイパー）で追跡されているすべての注文IDの集合を事前に作成する
	allTrackedIDs := make(map[string]bool)
	for _, orders := range s.sniperActiveOrders {
		for _, o := range orders {
			allTrackedIDs[o.ID] = true
		}
	}
	for _, tombstones := range s.tombstones {
		for _, t := range tombstones {
			allTrackedIDs[t.ord.ID] = true
		}
	}

	for sniperID, orders := range s.sniperActiveOrders {
		// 取引所レポートの中から、まだどのスナイパーにも追跡されていない該当銘柄の注文を探す
		var untrackedAPIOrders []*order.Order
		for i := range report.Orders {
			ext := &report.Orders[i]
			if ext.Symbol == s.Detail.Code && !allTrackedIDs[ext.ID] {
				untrackedAPIOrders = append(untrackedAPIOrders, ext)
			}
		}

		// 墓標（tombstones）にある注文のうち、IDが取引所レポートに存在しないものを対象に、
		// 未トラッキングの取引所注文とのファジーマッチを行う
		for _, t := range s.tombstones[sniperID] {
			if !reportContainsID(report, t.ord.ID) {
				for i, ext := range untrackedAPIOrders {
					if ext != nil &&
						t.ord.Symbol == ext.Symbol &&
						t.ord.Action == ext.Action &&
						t.ord.OrderQty == ext.OrderQty &&
						t.ord.OrderPrice == ext.OrderPrice {

						s.Logger.Info("🎯 [ID_RESOLVED] 送信エラーだった墓標注文が取引所注文と一致しました。IDを更新します",
							slog.String("sniper", sniperID),
							slog.String("localID", t.ord.ID),
							slog.String("serverID", ext.ID),
						)
						t.ord.ID = ext.ID
						untrackedAPIOrders[i] = nil // 使用済みマーク
						// 新しく紐付いたIDを追跡済みに追加して、他のマッチングでの重複を防ぐ
						allTrackedIDs[ext.ID] = true
						break
					}
				}
			}
		}

		// activeOrders に tombstones をマージして Reconcile を行います
		combined := make([]*order.Order, len(orders))
		copy(combined, orders)

		tombMap := make(map[string]bool)
		for _, t := range s.tombstones[sniperID] {
			combined = append(combined, t.ord)
			tombMap[t.ord.ID] = true
		}

		reconciled, newExecs := order.ReconcileOrders(combined, report, s.Detail.Code, s.processedExecutions, now)

		// 突き合わせた結果をアクティブと墓標に再分配します
		var nextActive []*order.Order
		var nextTombstones []tombstoneEntry

		// 元の墓標のうち、期限切れになっていないものを一時保持
		for _, t := range s.tombstones[sniperID] {
			if now.Sub(t.deletedAt) < 30*time.Second {
				nextTombstones = append(nextTombstones, t)
			}
		}

		for _, o := range reconciled {
			if tombMap[o.ID] {
				// 墓標にいた注文が取引所レポートに掲載されていた（または約定が走った）場合 -> 復活！
				s.Logger.Info("🎯 [TOMBSTONE_RESURRECTED] 一時削除された注文が取引所レポートで検知されたため、アクティブに復活させます",
					slog.String("sniper", sniperID),
					slog.String("orderID", o.ID),
					slog.String("status", fmt.Sprintf("%v", o.Status())),
				)
				nextActive = append(nextActive, o)
				// 復活したので墓標リストから削除
				var cleanedTomb []tombstoneEntry
				for _, t := range nextTombstones {
					if t.ord.ID != o.ID {
						cleanedTomb = append(cleanedTomb, t)
					}
				}
				nextTombstones = cleanedTomb
			} else {
				// 通常のアクティブ注文
				nextActive = append(nextActive, o)
			}
		}

		s.sniperActiveOrders[sniperID] = nextActive
		s.tombstones[sniperID] = nextTombstones

		for _, pe := range newExecs {
			s.applyExecution(sniperID, pe.Execution, pe.Action, pe.OrderCreatedAt, pe.ParentOrder)
			pe.ParentOrder.AddExecution(pe.Execution)
		}
	}
}

func (s *Spotter) GetPerformance(sniperID string) Performance {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sniperPerformance[sniperID]
}

func (s *Spotter) GetUnrealizedPnL(sniperID string, currentPrice float64) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	var unrealized float64
	for _, p := range s.sniperPositions[sniperID] {
		pnlFactor := 1.0
		if p.Action == order.ACTION_SELL {
			pnlFactor = -1.0
		}
		unrealized += (currentPrice - p.Price) * p.LeavesQty * pnlFactor
	}
	return unrealized
}

// AddOrder は新規注文を追跡対象に追加します
func (s *Spotter) AddOrder(sniperID string, ord *order.Order) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sniperActiveOrders[sniperID] = append(s.sniperActiveOrders[sniperID], ord)
}

// GetActiveOrders はすべてのアクティブな注文のリストを返します
func (s *Spotter) GetActiveOrders() []*order.Order {
	s.mu.Lock()
	defer s.mu.Unlock()
	var all []*order.Order
	for _, orders := range s.sniperActiveOrders {
		all = append(all, orders...)
	}
	return all
}

// GetSniperActiveOrders は特定のスナイパーのアクティブな注文リストを返します
func (s *Spotter) GetSniperActiveOrders(sniperID string) []*order.Order {
	s.mu.Lock()
	defer s.mu.Unlock()
	orders := s.sniperActiveOrders[sniperID]
	ordersCopy := make([]*order.Order, len(orders))
	copy(ordersCopy, orders)
	return ordersCopy
}

// FailSendingOrder は発注失敗した注文をアクティブリストから除外しますが、
// 証券会社側の遅延反映を考慮し、一時的に墓標（tombstone）リストに30秒間退避させます。
func (s *Spotter) FailSendingOrder(sniperID string, ord *order.Order) {
	s.mu.Lock()
	defer s.mu.Unlock()
	orders := s.sniperActiveOrders[sniperID]
	for i, o := range orders {
		if o == ord {
			s.sniperActiveOrders[sniperID] = append(orders[:i], orders[i+1:]...)
			s.tombstones[sniperID] = append(s.tombstones[sniperID], tombstoneEntry{
				ord:       o,
				deletedAt: time.Now(),
			})
			break
		}
	}
}

// UpdateOrderID はローカルIDから確定した取引所IDへ注文IDを更新します
func (s *Spotter) UpdateOrderID(sniperID string, ord *order.Order, newID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	orders := s.sniperActiveOrders[sniperID]
	for _, o := range orders {
		if o == ord || o.ID == ord.ID {
			o.ID = newID
			break
		}
	}
}

// RevertOrderStatus は注文ステータスを強制的にロールバックします（ゾンビ修復用）
func (s *Spotter) RevertOrderStatus(sniperID string, ord *order.Order, status order.OrderStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	orders := s.sniperActiveOrders[sniperID]
	for _, o := range orders {
		if o == ord || o.ID == ord.ID {
			o.BypassTransition(status, o.InternalState())
			break
		}
	}
}

// PrepareObservation は最新の Tick をもとに、指定した Sniper に渡すためのスナップショットを作成します。
func (s *Spotter) PrepareObservation(sniperID string, t tick.Tick, policy strategy.ExecutionPolicy) Observation {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. アクティブ注文の状態クリーンアップと疑似約定の適用
	var reconciled []*order.Order
	var hasProcessingTrade bool
	var blockingOrder *order.Order

	orders := s.sniperActiveOrders[sniperID]
	for _, curr := range orders {
		if curr.IsCompleted() {
			// 親注文が約定完了している場合、子注文(IfDone)があれば追跡を開始する
			if curr.IsFilled() && curr.IfDone != nil {
				child := curr.IfDone
				curr.IfDone = nil
				reconciled = append(reconciled, child)
				hasProcessingTrade = true
			}
			continue
		}

		reconciled = append(reconciled, curr)
		if curr.InternalState() != order.STATE_PREPARING {
			hasProcessingTrade = true
		}

		if policy != nil && !curr.IsPending() && !curr.IsCancelSent() && !curr.IsCompleted() {
			policy.ApplySyntheticFill(curr, t)
		}

		if !curr.IsCompleted() && curr.InternalState() != order.STATE_PREPARING {
			blockingOrder = curr
		}
	}
	s.sniperActiveOrders[sniperID] = reconciled

	// 建玉のコピー
	pos := s.sniperPositions[sniperID]
	posCopy := make([]position.Position, len(pos))
	copy(posCopy, pos)

	return Observation{
		Tick:               t,
		Positions:          posCopy,
		Performance:        s.sniperPerformance[sniperID],
		ActiveOrders:       reconciled,
		HasProcessingTrade: hasProcessingTrade,
		BlockingOrder:      blockingOrder,
	}
}

func (s *Spotter) applyExecution(sniperID string, exec order.Execution, action order.Action, orderCreatedAt time.Time, parentOrder *order.Order) {
	if s.processedExecutions[exec.ID] {
		return
	}
	s.processedExecutions[exec.ID] = true

	isExit := false
	exchange := order.EXCHANGE_TOSHO
	tradeType := order.TRADE_TYPE_GENERAL_DAY // 🌟 安全なデフォルト値（一般信用デイトレ）
	accountType := order.ACCOUNT_SPECIAL      // 🌟 安全なデフォルト値（特定口座）

	if parentOrder != nil {
		isExit = (parentOrder.CashMargin == order.CASH_MARGIN_MARGIN_EXIT)
		if parentOrder.Request != nil {
			exchange = parentOrder.Request.Exchange
			tradeType = parentOrder.Request.MarginTradeType
			accountType = parentOrder.Request.AccountType
		}
	}

	if !isExit {
		// 新規建てエントリー（信用新規買い、または信用新規売り）
		s.sniperPositions[sniperID] = append(s.sniperPositions[sniperID], position.Position{
			ExecutionID: exec.ID,
			Symbol:      s.Detail.Code,
			Exchange:    exchange,
			Action:      action,
			TradeType:   tradeType,
			AccountType: accountType,
			LeavesQty:   exec.Qty,
			Price:       exec.Price,
			Meta:        position.PositionMeta{EntryTime: exec.ExecutionTime},
		})
		s.Logger.Info("FILLED",
			slog.String("sniper", sniperID),
			slog.String("symbol", s.Detail.Code),
			slog.String("action", string(action)),
			slog.Float64("qty", exec.Qty),
			slog.Float64("price", exec.Price),
			slog.String("exit_reason", func() string {
				if parentOrder != nil {
					return parentOrder.Reason
				}
				return ""
			}()), // 🌟 理由を記録
		)
	} else {
		// 返済決済（信用返済売り、または信用返済買い）
		var closePositions []order.ClosePosition
		reason := ""
		if parentOrder != nil {
			if parentOrder.Request != nil {
				closePositions = parentOrder.Request.ClosePositions
			}
			reason = parentOrder.Reason
		}
		s.reducePositions(sniperID, exec.Qty, exec.Price, exec.ExecutionTime, closePositions, reason)
	}
}

func (s *Spotter) reducePositions(sniperID string, sellQty float64, sellPrice float64, sellTime time.Time, closePositions []order.ClosePosition, closeReason string) {
	remainingToSell := sellQty
	var totalTradePnL float64
	var earliestEntryTime time.Time

	positions := s.sniperPositions[sniperID]

	// 1. 指定返済優先
	if len(closePositions) > 0 {
		closeMap := make(map[string]float64)
		for _, cp := range closePositions {
			closeMap[cp.HoldID] = cp.Qty
		}

		var newPositions []position.Position
		for _, p := range positions {
			qtyToClose, exists := closeMap[p.ExecutionID]
			if exists && qtyToClose > 0 && remainingToSell > 0 {
				closeQty := p.LeavesQty
				if closeQty > qtyToClose { closeQty = qtyToClose }
				if closeQty > remainingToSell { closeQty = remainingToSell }

				if earliestEntryTime.IsZero() || (!p.Meta.EntryTime.IsZero() && p.Meta.EntryTime.Before(earliestEntryTime)) {
					earliestEntryTime = p.Meta.EntryTime
				}

				pnlFactor := 1.0
				if p.Action == order.ACTION_SELL {
					pnlFactor = -1.0
				}
				tradePnL := (sellPrice - p.Price) * closeQty * pnlFactor
				totalTradePnL += tradePnL
				s.updatePerformance(sniperID, tradePnL)

				p.LeavesQty -= closeQty
				closeMap[p.ExecutionID] -= closeQty
				remainingToSell -= closeQty

				if p.LeavesQty > 0 { newPositions = append(newPositions, p) }
			} else {
				newPositions = append(newPositions, p)
			}
		}
		positions = newPositions
	}

	// 2. FIFO削減
	if remainingToSell > 0 {
		var newPositions []position.Position
		for _, p := range positions {
			if remainingToSell <= 0 {
				newPositions = append(newPositions, p)
				continue
			}

			closeQty := p.LeavesQty
			if closeQty > remainingToSell { closeQty = remainingToSell }

			if earliestEntryTime.IsZero() || (!p.Meta.EntryTime.IsZero() && p.Meta.EntryTime.Before(earliestEntryTime)) {
				earliestEntryTime = p.Meta.EntryTime
			}

			pnlFactor := 1.0
			if p.Action == order.ACTION_SELL {
				pnlFactor = -1.0
			}
			tradePnL := (sellPrice - p.Price) * closeQty * pnlFactor
			totalTradePnL += tradePnL
			s.updatePerformance(sniperID, tradePnL)

			if p.LeavesQty <= remainingToSell {
				remainingToSell -= p.LeavesQty
			} else {
				p.LeavesQty -= remainingToSell
				remainingToSell = 0
				newPositions = append(newPositions, p)
			}
		}
		positions = newPositions
	}
	s.sniperPositions[sniperID] = positions

	holdTimeSec := 0.0
	if !earliestEntryTime.IsZero() && !sellTime.IsZero() {
		holdTimeSec = sellTime.Sub(earliestEntryTime).Seconds()
	}
	s.Logger.Info("POSITION_CLOSED",
		slog.String("sniper", sniperID),
		slog.String("symbol", s.Detail.Code),
		slog.Float64("pnl", totalTradePnL),
		slog.Float64("hold_time_sec", holdTimeSec),
		slog.String("exit_reason", closeReason),
		slog.Time("entry_time", earliestEntryTime), // 🌟 追加
		slog.Time("exit_time", sellTime),           // 🌟 追加
	)
}

func (s *Spotter) updatePerformance(sniperID string, pnl float64) {
	perf := s.sniperPerformance[sniperID]
	perf.RealizedPnL += pnl
	perf.Trades++
	if pnl > 0 {
		perf.Wins++
	} else if pnl < 0 {
		perf.Losses++
	}
	s.sniperPerformance[sniperID] = perf
}

func (s *Spotter) HoldQty(sniperID string) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var total float64
	for _, p := range s.sniperPositions[sniperID] {
		if p.Action == order.ACTION_SELL {
			total -= p.LeavesQty
		} else {
			total += p.LeavesQty
		}
	}
	return total
}


