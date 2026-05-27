package sniper

import (
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/position"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

// Observation は Spotter が観測し、整理した「現在の事実」です。
// Sniper はこれを受け取って判断を下します。
type Observation struct {
	Tick        tick.Tick
	Positions   []position.Position
	Orders      []*order.Order
	Performance Performance
}

// HoldQty は現在の事実上の保有数量を返します
func (o Observation) HoldQty() float64 {
	var total float64
	for _, p := range o.Positions {
		total += p.LeavesQty
	}
	return total
}

// Spotter は特定の銘柄の「現実の状態（事実）」を監視・維持する役割を担います。
type Spotter struct {
	Detail              symbol.Symbol
	sniperPositions     map[string][]position.Position
	sniperOrders        map[string][]*order.Order
	sniperPerformance   map[string]Performance
	processedExecutions map[string]bool
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
		sniperOrders:        make(map[string][]*order.Order),
		sniperPerformance:   make(map[string]Performance),
		processedExecutions: make(map[string]bool),
		Logger:              logger,
	}
}

// RecordBullet は Sniper が発行した判断（Bullet）を記録し、注文とスナイパーを紐付けます。
func (s *Spotter) RecordBullet(sniperID string, bullet Bullet) {
	if bullet.Order == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sniperOrders[sniperID] = append(s.sniperOrders[sniperID], bullet.Order)
}

// Update は API からの注文レポートを受け取り、内部の「事実」を更新します。
func (s *Spotter) Update(report order.Orders, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. 各スナイパーごとに注文状態の整理（Reconciliation）
	for sniperID, orders := range s.sniperOrders {
		var reconciledOrders []*order.Order
		// 未完了注文の保持（APIへの反映待ち含む）
		for _, o := range orders {
			if o.IsPending() {
				if now.Sub(o.CreatedAt) < 30*time.Second {
					reconciledOrders = append(reconciledOrders, o)
				}
			} else if !o.IsCompleted() {
				reconciledOrders = append(reconciledOrders, o)
			}
		}

		// APIレポートの反映
		var pendingExecs []struct {
			exec           order.Execution
			action         order.Action
			orderCreatedAt time.Time
			parentOrder    *order.Order
			sniperID       string
		}

		for _, ext := range report.Orders {
			if ext.Symbol != s.Detail.Code {
				continue
			}

			var matchedInternal *order.Order
			// このスナイパーの注文リストから探す
			for _, o := range reconciledOrders {
				if o.ID == ext.ID {
					matchedInternal = o
					break
				}
			}
			if matchedInternal == nil {
				for _, o := range orders {
					if o.ID == ext.ID {
						matchedInternal = o
						break
					}
				}
			}

			if matchedInternal == nil {
				continue
			}

			// 状態同期
			matchedInternal.Status = ext.Status
			matchedInternal.CumQty = ext.CumQty
			if matchedInternal.IsPending() {
				matchedInternal.InternalState = order.STATE_ACTIVE
			}

			// 約定の抽出
			for _, exec := range ext.Executions {
				if !s.processedExecutions[exec.ID] {
					pendingExecs = append(pendingExecs, struct {
						exec           order.Execution
						action         order.Action
						orderCreatedAt time.Time
						parentOrder    *order.Order
						sniperID       string
					}{
						exec:           exec,
						action:         matchedInternal.Action,
						orderCreatedAt: matchedInternal.CreatedAt,
						parentOrder:    matchedInternal,
						sniperID:       sniperID,
					})
				}
			}

			// 完了した注文も、レポートにある限りは保持する
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

		// 完了済み注文の整理（ゾンビ管理）
		var activeOrders []*order.Order
		var completedButNotInReport []*order.Order

		for _, o := range reconciledOrders {
			inReport := false
			for _, ext := range report.Orders {
				if ext.ID == o.ID {
					inReport = true
					break
				}
			}

			if !o.IsCompleted() || inReport {
				activeOrders = append(activeOrders, o)
			} else {
				completedButNotInReport = append(completedButNotInReport, o)
			}
		}

		sort.Slice(completedButNotInReport, func(i, j int) bool {
			return completedButNotInReport[i].CreatedAt.After(completedButNotInReport[j].CreatedAt)
		})
		if len(completedButNotInReport) > 10 {
			completedButNotInReport = completedButNotInReport[:10]
		}

		s.sniperOrders[sniperID] = append(activeOrders, completedButNotInReport...)

		// 2. 約定の反映（時系列順）
		sort.Slice(pendingExecs, func(i, j int) bool {
			return pendingExecs[i].exec.ExecutionTime.Before(pendingExecs[j].exec.ExecutionTime)
		})

		for _, pe := range pendingExecs {
			s.applyExecution(pe.sniperID, pe.exec, pe.action, pe.orderCreatedAt, pe.parentOrder)
			pe.parentOrder.AddExecution(pe.exec)
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
		unrealized += (currentPrice - p.Price) * p.LeavesQty
	}
	return unrealized
}

// PrepareObservation は最新の Tick をもとに、指定した Sniper に渡すためのスナップショットを作成します。
func (s *Spotter) PrepareObservation(sniperID string, t tick.Tick) Observation {
	s.mu.Lock()
	defer s.mu.Unlock()

	pos := s.sniperPositions[sniperID]
	posCopy := make([]position.Position, len(pos))
	copy(posCopy, pos)

	ords := s.sniperOrders[sniperID]
	orderCopy := make([]*order.Order, len(ords))
	copy(orderCopy, ords)

	return Observation{
		Tick:        t,
		Positions:   posCopy,
		Orders:      orderCopy,
		Performance: s.sniperPerformance[sniperID],
	}
}

func (s *Spotter) applyExecution(sniperID string, exec order.Execution, action order.Action, orderCreatedAt time.Time, parentOrder *order.Order) {
	if s.processedExecutions[exec.ID] {
		return
	}
	s.processedExecutions[exec.ID] = true

	switch action {
	case order.ACTION_BUY:
		s.sniperPositions[sniperID] = append(s.sniperPositions[sniperID], position.Position{
			ExecutionID: exec.ID,
			Symbol:      s.Detail.Code,
			LeavesQty:   exec.Qty,
			Price:       exec.Price,
			Meta:        position.PositionMeta{EntryTime: exec.ExecutionTime},
		})
		s.Logger.Info("FILLED",
			slog.String("sniper", sniperID),
			slog.String("symbol", s.Detail.Code),
			slog.Float64("qty", exec.Qty),
			slog.Float64("price", exec.Price),
			slog.String("exit_reason", parentOrder.Reason), // 🌟 理由を記録
		)
	case order.ACTION_SELL:
		var closePositions []order.ClosePosition
		reason := ""
		if parentOrder != nil {
			closePositions = parentOrder.ClosePositions
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

				tradePnL := (sellPrice - p.Price) * closeQty
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

			tradePnL := (sellPrice - p.Price) * closeQty
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

// AddOrder は後方互換性のために残していますが、基本的には RecordBullet を使用してください。
func (s *Spotter) AddOrder(o *order.Order) {
	// SniperIDが不明なため、"default" グループに記録
	s.RecordBullet("default", Bullet{Order: o})
}
