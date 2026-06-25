package sniper

import (
	"log/slog"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

type tombstoneEntry struct {
	ord       *order.Order
	deletedAt time.Time
}

// OrderTracker handles active orders, tombstones recovery, and execution deduplication.
//
// DESIGN DECISION:
// Although storing order status (activeOrders, tombstones) and updating/reconciling them
// (processedExecutions, Update) have conceptually different natures (data store vs. reconciliation engine),
// they are kept unified within OrderTracker. This represents a single domain responsibility:
// "managing the lifecycle and synchronization of active orders". Keeping them unified prevents
// boilerplate mutation APIs and simplifies package-internal state management.
type OrderTracker struct {
	activeOrders        map[string][]*order.Order
	tombstones          map[string][]tombstoneEntry
	processedExecutions map[string]bool
	logger              *slog.Logger
}

func NewOrderTracker(logger *slog.Logger) *OrderTracker {
	return &OrderTracker{
		activeOrders:        make(map[string][]*order.Order),
		tombstones:          make(map[string][]tombstoneEntry),
		processedExecutions: make(map[string]bool),
		logger:              logger,
	}
}

func (ot *OrderTracker) Add(sniperID string, ord *order.Order) {
	ot.activeOrders[sniperID] = append(ot.activeOrders[sniperID], ord)
}

func (ot *OrderTracker) GetActive(sniperID string) []*order.Order {
	orders := ot.activeOrders[sniperID]
	ordersCopy := make([]*order.Order, len(orders))
	copy(ordersCopy, orders)
	return ordersCopy
}

func (ot *OrderTracker) GetAllActive() []*order.Order {
	var all []*order.Order
	for _, orders := range ot.activeOrders {
		all = append(all, orders...)
	}
	return all
}

func (ot *OrderTracker) FailOrder(sniperID string, ord *order.Order) bool {
	orders := ot.activeOrders[sniperID]
	for i, o := range orders {
		if o == ord {
			ot.activeOrders[sniperID] = append(orders[:i], orders[i+1:]...)
			ot.tombstones[sniperID] = append(ot.tombstones[sniperID], tombstoneEntry{
				ord:       o,
				deletedAt: time.Now(),
			})
			return true
		}
	}
	return false
}

func (ot *OrderTracker) UpdateOrderID(sniperID string, ord *order.Order, newID string) {
	orders := ot.activeOrders[sniperID]
	for _, o := range orders {
		if o == ord || o.ID == ord.ID {
			o.ID = newID
			break
		}
	}
}

func (ot *OrderTracker) RevertOrderStatus(sniperID string, ord *order.Order, status order.OrderStatus) {
	orders := ot.activeOrders[sniperID]
	for _, o := range orders {
		if o == ord || o.ID == ord.ID {
			o.BypassTransition(status, o.InternalState())
			break
		}
	}
}

func (ot *OrderTracker) IsExecutionProcessed(id string) bool {
	return ot.processedExecutions[id]
}

func (ot *OrderTracker) MarkExecutionProcessed(id string) {
	ot.processedExecutions[id] = true
}

func (ot *OrderTracker) Update(report order.Orders, detail symbol.Symbol, now time.Time, onExecution func(sniperID string, exec order.Execution, action order.Action, orderCreatedAt time.Time, parentOrder *order.Order)) {
	allTrackedIDs := make(map[string]bool)
	for _, orders := range ot.activeOrders {
		for _, o := range orders {
			allTrackedIDs[o.ID] = true
		}
	}
	for _, tombstones := range ot.tombstones {
		for _, t := range tombstones {
			allTrackedIDs[t.ord.ID] = true
		}
	}

	for sniperID, orders := range ot.activeOrders {
		var untrackedAPIOrders []*order.Order
		for i := range report.Orders {
			ext := &report.Orders[i]
			if ext.Symbol == detail.Code && !allTrackedIDs[ext.ID] {
				untrackedAPIOrders = append(untrackedAPIOrders, ext)
			}
		}

		for _, o := range orders {
			if o.IfDone != nil && o.IfDone.IsPending() {
				for i, ext := range untrackedAPIOrders {
					if ext != nil && ext.ParentOrderID == o.ID {
						if ext.OrderQty <= o.IfDone.OrderQty {
							if ot.logger != nil {
								ot.logger.Info("🎯 [ID_RESOLVED] IFD子注文の発注を検知しました",
									slog.String("sniper", sniperID),
									slog.Float64("qty", ext.OrderQty),
									slog.String("serverID", ext.ID),
								)
							}

							matchedChild := order.NewOrder(
								ext.ID,
								o.IfDone.Symbol,
								o.IfDone.Action,
								o.IfDone.OrderPrice,
								ext.OrderQty,
								order.WithType(o.IfDone.Type),
								order.WithCashMargin(o.IfDone.CashMargin),
								order.WithRequest(ext.Request),
								order.WithReason(o.IfDone.Reason),
							)
							matchedChild.BypassTransition(ext.Status(), order.STATE_ACTIVE)
							ot.activeOrders[sniperID] = append(ot.activeOrders[sniperID], matchedChild)
							orders = ot.activeOrders[sniperID]

							o.IfDone.OrderQty -= ext.OrderQty
							if o.IfDone.OrderQty <= 0 {
								o.IfDone = nil
							}

							untrackedAPIOrders[i] = nil
							allTrackedIDs[ext.ID] = true
							break
						}
					}
				}
			}
		}

		// 1. Identify which tombstone orders should be resurrected (those that actually exist in the API report)
		var resurrected []*order.Order
		resurrectedMap := make(map[string]bool)

		for _, t := range ot.tombstones[sniperID] {
			// A. If the server ID already exists in the report, it is resurrected
			if reportContainsID(report, t.ord.ID) {
				resurrected = append(resurrected, t.ord)
				resurrectedMap[t.ord.ID] = true
				continue
			}

			// B. If it is a local ID, try to match it with untracked API orders
			for i, ext := range untrackedAPIOrders {
				if ext != nil &&
					t.ord.Symbol == ext.Symbol &&
					t.ord.Action == ext.Action &&
					t.ord.OrderQty == ext.OrderQty &&
					t.ord.OrderPrice == ext.OrderPrice {

					// API注文の作成時刻が現在（ポーリング時刻）から60秒以内のもののみマッチングを許可（前日等の古い注文の誤マッチングを防ぐ）
					timeDiff := now.Sub(ext.CreatedAt)
					if timeDiff < 0 {
						timeDiff = -timeDiff
					}
					if timeDiff > 60*time.Second {
						continue
					}

					if ot.logger != nil {
						ot.logger.Info("🎯 [ID_RESOLVED] 送信エラーだった墓標注文が一致しました",
							slog.String("sniper", sniperID),
							slog.String("localID", t.ord.ID),
							slog.String("serverID", ext.ID),
						)
					}
					t.ord.ID = ext.ID
					untrackedAPIOrders[i] = nil
					allTrackedIDs[ext.ID] = true

					resurrected = append(resurrected, t.ord)
					resurrectedMap[ext.ID] = true
					break
				}
			}
		}

		// 2. Only merge verified active orders and resurrected orders to pass to ReconcileOrders
		combined := make([]*order.Order, len(orders))
		copy(combined, orders)
		combined = append(combined, resurrected...)

		reconciled, newExecs := order.ReconcileOrders(combined, report, detail.Code, ot.processedExecutions, now)

		// 3. Clean up the tombstones list (remove resurrected ones and keep only ones created within 30s)
		var nextTombstones []tombstoneEntry
		for _, t := range ot.tombstones[sniperID] {
			if resurrectedMap[t.ord.ID] {
				if ot.logger != nil {
					ot.logger.Info("🎯 [TOMBSTONE_RESURRECTED] 復活を検知",
						slog.String("sniper", sniperID),
						slog.String("orderID", t.ord.ID),
					)
				}
				continue
			}
			if now.Sub(t.deletedAt) < 30*time.Second {
				nextTombstones = append(nextTombstones, t)
			}
		}

		ot.activeOrders[sniperID] = reconciled
		ot.tombstones[sniperID] = nextTombstones

		for _, pe := range newExecs {
			onExecution(sniperID, pe.Execution, pe.Action, pe.OrderCreatedAt, pe.ParentOrder)
			pe.ParentOrder.AddExecution(pe.Execution)
		}
	}
}

// PrepareActiveOrders filters completed orders, promotes IFD child orders, and applies synthetic fills.
func (ot *OrderTracker) PrepareActiveOrders(sniperID string, t tick.Tick, policy strategy.ExecutionPolicy) ([]*order.Order, bool, *order.Order) {
	var reconciled []*order.Order
	var hasProcessingTrade bool
	var blockingOrder *order.Order

	orders := ot.activeOrders[sniperID]
	for _, curr := range orders {
		if curr.IsCompleted() {
			if curr.IsFilled() && curr.IfDone != nil {
				if curr.IfDone.InternalState() == order.STATE_PREPARING {
					reconciled = append(reconciled, curr)
					hasProcessingTrade = true
					continue
				}
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
	ot.activeOrders[sniperID] = reconciled

	return reconciled, hasProcessingTrade, blockingOrder
}

// InflightStats holds aggregated stats for active orders of a sniper.
type InflightStats struct {
	InflightBuyEntry  float64
	InflightSellEntry float64
	InflightBuyExit   float64
	InflightSellExit  float64
	ActiveOrders      []*order.Order
	PreparingOrder    *order.Order
	OutstandingOrder  *order.Order
	CancelingOrders   []*order.Order
}

// GetInflightStats aggregates and categorizes active orders for a sniper.
func (ot *OrderTracker) GetInflightStats(sniperID string) InflightStats {
	var stats InflightStats
	orders := ot.activeOrders[sniperID]

	// Build a map of execution IDs already covered by active/pending exit orders
	coveredExecIDs := make(map[string]bool)
	for _, o := range orders {
		if o == nil || o.IsCompleted() || o.IsCancelSent() {
			continue
		}
		if o.CashMargin == order.CASH_MARGIN_MARGIN_EXIT && o.Request != nil {
			for _, cp := range o.Request.ClosePositions {
				coveredExecIDs[cp.HoldID] = true
			}
		}
	}

	for _, o := range orders {
		if o == nil {
			continue
		}

		// Track unmatched child exit orders for parent orders that have executions
		if o.IfDone != nil {
			for _, exec := range o.Executions {
				if !coveredExecIDs[exec.ID] {
					if o.IfDone.CashMargin == order.CASH_MARGIN_MARGIN_EXIT {
						if o.IfDone.Action == order.ACTION_BUY {
							stats.InflightBuyExit += exec.Qty
						} else if o.IfDone.Action == order.ACTION_SELL {
							stats.InflightSellExit += exec.Qty
						}
					}
				}
			}
		}

		if o.IsCompleted() {
			continue
		}

		if o.IsCancelSent() {
			stats.CancelingOrders = append(stats.CancelingOrders, o)
			continue
		}

		stats.ActiveOrders = append(stats.ActiveOrders, o)

		if o.InternalState() == order.STATE_PREPARING {
			stats.PreparingOrder = o
		} else {
			stats.OutstandingOrder = o
		}

		// Sum up inflight quantities (excluding orders expected to fill synthetically as they are already accounted for)
		if !o.IsFillExpected() {
			if o.CashMargin == order.CASH_MARGIN_MARGIN_ENTRY {
				if o.Action == order.ACTION_BUY {
					stats.InflightBuyEntry += o.OrderQty
				} else if o.Action == order.ACTION_SELL {
					stats.InflightSellEntry += o.OrderQty
				}
			} else if o.CashMargin == order.CASH_MARGIN_MARGIN_EXIT {
				if o.Action == order.ACTION_BUY {
					stats.InflightBuyExit += o.OrderQty
				} else if o.Action == order.ACTION_SELL {
					stats.InflightSellExit += o.OrderQty
				}
			}
		} else {
			// If the order is expected to fill synthetically, its IfDone exits are also expected to activate
			if o.IfDone != nil {
				if o.IfDone.CashMargin == order.CASH_MARGIN_MARGIN_EXIT {
					if o.IfDone.Action == order.ACTION_BUY {
						stats.InflightBuyExit += o.IfDone.OrderQty
					} else if o.IfDone.Action == order.ACTION_SELL {
						stats.InflightSellExit += o.IfDone.OrderQty
					}
				}
			}
		}
	}
	return stats
}
