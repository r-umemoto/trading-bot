package sniper

import (
	"log/slog"
	"strings"
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

type pendingExecEntry struct {
	sniperID       string
	execution      *order.Execution
	action         order.Action
	orderCreatedAt time.Time
	parentOrder    *order.Order
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
	pendingExits        map[string][]pendingExecEntry // Key: HoldID -> Value: 保留されている約定情報のリスト
}

func NewOrderTracker(logger *slog.Logger) *OrderTracker {
	return &OrderTracker{
		activeOrders:        make(map[string][]*order.Order),
		tombstones:          make(map[string][]tombstoneEntry),
		processedExecutions: make(map[string]bool),
		logger:              logger,
		pendingExits:        make(map[string][]pendingExecEntry),
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

// DestroyOrder completely deletes an order from activeOrders without placing it into Tombstones.
// Used for definitive API reject errors (e.g. buying power insufficient).
func (ot *OrderTracker) DestroyOrder(sniperID string, ord *order.Order) bool {
	orders := ot.activeOrders[sniperID]
	for i, o := range orders {
		if o == ord {
			ot.activeOrders[sniperID] = append(orders[:i], orders[i+1:]...)
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
			isEntry := true
			if pe.ParentOrder != nil {
				isEntry = pe.ParentOrder.CashMargin != order.CASH_MARGIN_MARGIN_EXIT
			}

			if isEntry {
				// --- 1. 親約定（ENTRY）の場合 ---
				// A. まず親約定をそのまま Nest コールバックして適用
				onExecution(sniperID, pe.Execution, pe.Action, pe.OrderCreatedAt, pe.ParentOrder)
				pe.ParentOrder.AddExecution(pe.Execution)
				ot.processedExecutions[pe.Execution.ID] = true

				// B. 保留されている決済約定があれば解決する
				if pending, exists := ot.pendingExits[pe.Execution.ID]; exists {
					parentRemainingQty := pe.Execution.Qty
					for _, pEntry := range pending {
						if parentRemainingQty <= 0 {
							break
						}

						// 1. 今届いた親の分の解決
						closeQty := pEntry.execution.Qty
						if pEntry.parentOrder != nil && pEntry.parentOrder.Request != nil {
							for _, cp := range pEntry.parentOrder.Request.ClosePositions {
								if cp.HoldID == pe.Execution.ID {
									closeQty = cp.Qty
									break
								}
							}
						}

						if closeQty > parentRemainingQty {
							closeQty = parentRemainingQty
						}
						if closeQty > pEntry.execution.Qty {
							closeQty = pEntry.execution.Qty
						}

						if closeQty > 0 {
							execToSend := *pEntry.execution
							execToSend.Qty = closeQty
							// 🌟 重複処理ガードを回避するため、HoldID を付与して約定IDをユニーク化
							execToSend.ID = execToSend.ID + "-" + pe.Execution.ID

							// この HoldID (pe.Execution.ID) だけを指定した注文コピーを渡してピンポイントで消し込む
							orderCopy := makeCloseOrderCopy(pEntry.parentOrder, pe.Execution.ID, closeQty)

							onExecution(pEntry.sniperID, execToSend, pEntry.action, pEntry.orderCreatedAt, orderCopy)
							pEntry.parentOrder.AddExecution(execToSend)

							pEntry.execution.Qty -= closeQty
							parentRemainingQty -= closeQty
						}

						// 2. すでにメモリ上に存在している他の親（processedExecutions に入っているもの）についても、この約定の残り数量を使って解決する
						if pEntry.execution.Qty > 0 && pEntry.parentOrder != nil && pEntry.parentOrder.Request != nil {
							for _, cp := range pEntry.parentOrder.Request.ClosePositions {
								if pEntry.execution.Qty <= 0 {
									break
								}
								if cp.HoldID != pe.Execution.ID && ot.processedExecutions[cp.HoldID] {
									otherCloseQty := cp.Qty
									if otherCloseQty > pEntry.execution.Qty {
										otherCloseQty = pEntry.execution.Qty
									}

									if otherCloseQty > 0 {
										execToSend := *pEntry.execution
										execToSend.Qty = otherCloseQty
										// 🌟 重複処理ガードを回避するため、HoldID を付与して約定IDをユニーク化
										execToSend.ID = execToSend.ID + "-" + cp.HoldID

										// この HoldID (cp.HoldID) だけを指定した注文コピーを渡してピンポイントで消し込む
										orderCopy := makeCloseOrderCopy(pEntry.parentOrder, cp.HoldID, otherCloseQty)

										onExecution(pEntry.sniperID, execToSend, pEntry.action, pEntry.orderCreatedAt, orderCopy)
										pEntry.parentOrder.AddExecution(execToSend)

										pEntry.execution.Qty -= otherCloseQty
									}
								}
							}
						}

						// 3. 決済約定を完全に使い切った場合の処理
						if pEntry.execution.Qty <= 0 {
							ot.processedExecutions[pEntry.execution.ID] = true

							// すべての保留キーからこの約定IDを一斉消去してクリーンアップ
							for k, list := range ot.pendingExits {
								if k == pe.Execution.ID {
									continue
								}
								var filteredList []pendingExecEntry
								for _, item := range list {
									if item.execution.ID != pEntry.execution.ID {
										filteredList = append(filteredList, item)
									}
								}
								if len(filteredList) == 0 {
									delete(ot.pendingExits, k)
								} else {
									ot.pendingExits[k] = filteredList
								}
							}
						}
					}
					delete(ot.pendingExits, pe.Execution.ID)
				}

			} else {
				// --- 2. 決済約定（EXIT）の場合 ---
				var missingHoldIDs []string
				if pe.ParentOrder != nil && pe.ParentOrder.Request != nil {
					for _, cp := range pe.ParentOrder.Request.ClosePositions {
						if !ot.processedExecutions[cp.HoldID] {
							missingHoldIDs = append(missingHoldIDs, cp.HoldID)
						}
					}
				}

				if len(missingHoldIDs) > 0 {
					// 親が1つでも未達なので、決済約定の適用を一切行わずに保留スタックに退避する
					// ヒープ上に約定の独立したコピーを明示的に確保
					execCopyPtr := new(order.Execution)
					*execCopyPtr = pe.Execution

					for _, holdID := range missingHoldIDs {
						ot.pendingExits[holdID] = append(ot.pendingExits[holdID], pendingExecEntry{
							sniperID:       sniperID,
							execution:      execCopyPtr,
							action:         pe.Action,
							orderCreatedAt: pe.OrderCreatedAt,
							parentOrder:    pe.ParentOrder,
						})
					}
				} else {
					// すべて揃っているので、そのまま適用
					onExecution(sniperID, pe.Execution, pe.Action, pe.OrderCreatedAt, pe.ParentOrder)
					pe.ParentOrder.AddExecution(pe.Execution)
					ot.processedExecutions[pe.Execution.ID] = true
				}
			}
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

		isVerifiedServerOrder := !strings.HasPrefix(curr.ID, order.LOCAL_ID_PREFIX)
		if policy != nil && !curr.IsPending() && !curr.IsCancelSent() && !curr.IsCompleted() && isVerifiedServerOrder {
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

func makeCloseOrderCopy(orig *order.Order, holdID string, qty float64) *order.Order {
	if orig == nil {
		return nil
	}
	orderCopy := *orig
	if orig.Request != nil {
		reqCopy := *orig.Request
		reqCopy.ClosePositions = []order.ClosePosition{
			{HoldID: holdID, Qty: qty},
		}
		orderCopy.Request = &reqCopy
	}
	return &orderCopy
}
