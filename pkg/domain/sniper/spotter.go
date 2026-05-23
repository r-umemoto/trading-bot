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
	Tick      tick.Tick
	Positions []position.Position
	Orders    []*order.Order
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
	positions           []position.Position
	orders              []*order.Order
	processedExecutions map[string]bool
	Performance         Performance
	Logger              *slog.Logger
	mu                  sync.Mutex
}

func NewSpotter(detail symbol.Symbol, logger *slog.Logger) *Spotter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Spotter{
		Detail:              detail,
		positions:           make([]position.Position, 0),
		orders:              make([]*order.Order, 0),
		processedExecutions: make(map[string]bool),
		Logger:              logger,
	}
}

// Update は API からの注文レポートを受け取り、内部の「事実」を更新します。
func (s *Spotter) Update(report order.Orders, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var pendingExecs []struct {
		exec           order.Execution
		action         order.Action
		orderCreatedAt time.Time
		parentOrder    *order.Order
	}

	var reconciledOrders []*order.Order

	// 1. 未完了注文の保持（APIへの反映待ち含む）
	for _, o := range s.orders {
		if o.IsPending() {
			// 送信中注文は30秒間保持
			if now.Sub(o.CreatedAt) < 30*time.Second {
				reconciledOrders = append(reconciledOrders, o)
			}
		} else if !o.IsCompleted() {
			reconciledOrders = append(reconciledOrders, o)
		}
	}

	// 2. APIレポートの反映
	for _, ext := range report.Orders {
		if ext.Symbol != s.Detail.Code {
			continue
		}

		var matchedInternal *order.Order
		for _, o := range s.orders {
			if o.ID == ext.ID {
				matchedInternal = o
				break
			}
		}

		if matchedInternal == nil {
			continue // 知らない注文は無視（他の戦略等）
		}

		// 状態同期（Sniper側での特殊状態 FILL_EXPECTED 等はここでは関知しない）
		matchedInternal.Status = ext.Status
		matchedInternal.CumQty = ext.CumQty
		if matchedInternal.IsPending() {
			matchedInternal.InternalState = order.STATE_ACTIVE
		}

		for _, exec := range ext.Executions {
			if !s.processedExecutions[exec.ID] {
				pendingExecs = append(pendingExecs, struct {
					exec           order.Execution
					action         order.Action
					orderCreatedAt time.Time
					parentOrder    *order.Order
				}{
					exec:           exec,
					action:         matchedInternal.Action,
					orderCreatedAt: matchedInternal.CreatedAt,
					parentOrder:    matchedInternal,
				})
			}
		}

		// 完了していない、または約定データが不足している注文を残す
		if !matchedInternal.IsCompleted() || matchedInternal.FilledQty() < matchedInternal.CumQty {
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

	s.orders = reconciledOrders

	// 3. 約定の反映（時系列順）
	sort.Slice(pendingExecs, func(i, j int) bool {
		return pendingExecs[i].exec.ExecutionTime.Before(pendingExecs[j].exec.ExecutionTime)
	})

	for _, pe := range pendingExecs {
		s.applyExecution(pe.exec, pe.action, pe.orderCreatedAt, pe.parentOrder)
		pe.parentOrder.AddExecution(pe.exec)
	}
}

// PrepareObservation は最新の Tick をもとに、Sniper に渡すためのスナップショットを作成します。
func (s *Spotter) PrepareObservation(t tick.Tick) Observation {
	s.mu.Lock()
	defer s.mu.Unlock()

	// スライスは参照渡しなので、内容が変わらないようコピーを作成して渡す
	posCopy := make([]position.Position, len(s.positions))
	copy(posCopy, s.positions)

	orderCopy := make([]*order.Order, len(s.orders))
	copy(orderCopy, s.orders)

	return Observation{
		Tick:      t,
		Positions: posCopy,
		Orders:    orderCopy,
	}
}

// applyExecution は個別の約定をポジションに反映します。
func (s *Spotter) applyExecution(exec order.Execution, action order.Action, orderCreatedAt time.Time, parentOrder *order.Order) {
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
			Meta:        position.PositionMeta{EntryTime: exec.ExecutionTime},
		})
		s.Logger.Info("FILLED",
			slog.String("symbol", s.Detail.Code),
			slog.String("event", "FILLED"),
			slog.Float64("qty", exec.Qty),
			slog.Float64("price", exec.Price),
		)
	case order.ACTION_SELL:
		var closePositions []order.ClosePosition
		if parentOrder != nil {
			closePositions = parentOrder.ClosePositions
		}
		s.reducePositions(exec.Qty, exec.Price, exec.ExecutionTime, closePositions)
	}
}

// reducePositions は売り約定に伴い、保有ポジションを削減し損益を計算します。
func (s *Spotter) reducePositions(sellQty float64, sellPrice float64, sellTime time.Time, closePositions []order.ClosePosition) {
	remainingToSell := sellQty
	var totalTradePnL float64
	var earliestEntryTime time.Time

	// 1. 指定返済優先
	if len(closePositions) > 0 {
		closeMap := make(map[string]float64)
		for _, cp := range closePositions {
			closeMap[cp.HoldID] = cp.Qty
		}

		var newPositions []position.Position
		for _, p := range s.positions {
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
				s.updatePerformance(tradePnL)

				p.LeavesQty -= closeQty
				closeMap[p.ExecutionID] -= closeQty
				remainingToSell -= closeQty

				if p.LeavesQty > 0 { newPositions = append(newPositions, p) }
			} else {
				newPositions = append(newPositions, p)
			}
		}
		s.positions = newPositions
	}

	// 2. FIFO削減
	if remainingToSell > 0 {
		var newPositions []position.Position
		for _, p := range s.positions {
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
			s.updatePerformance(tradePnL)

			if p.LeavesQty <= remainingToSell {
				remainingToSell -= p.LeavesQty
			} else {
				p.LeavesQty -= remainingToSell
				remainingToSell = 0
				newPositions = append(newPositions, p)
			}
		}
		s.positions = newPositions
	}

	holdTimeSec := 0.0
	if !earliestEntryTime.IsZero() && !sellTime.IsZero() {
		holdTimeSec = sellTime.Sub(earliestEntryTime).Seconds()
	}
	s.Logger.Info("POSITION_CLOSED",
		slog.String("symbol", s.Detail.Code),
		slog.String("event", "POSITION_CLOSED"),
		slog.Float64("pnl", totalTradePnL),
		slog.Float64("hold_time_sec", holdTimeSec),
	)
}

func (s *Spotter) updatePerformance(pnl float64) {
	s.Performance.RealizedPnL += pnl
	s.Performance.Trades++
	if pnl > 0 {
		s.Performance.Wins++
	} else if pnl < 0 {
		s.Performance.Losses++
	}
}

// AddOrder は Sniper が新規に発行した注文を、Spotter の管理対象（未反映リスト）に加えます。
func (s *Spotter) AddOrder(o *order.Order) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orders = append(s.orders, o)
}
