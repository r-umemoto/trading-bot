package sniper

import (
	"log/slog"
	"sync"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/position"
)

// PositionTracker tracks physical positions, close matching, and PnL for a symbol.
// It acts as a thread-safe repository and domain service managing a unified position pool for the nest.
type PositionTracker struct {
	mu        sync.RWMutex
	positions position.Positions
	logger    *slog.Logger
}

func NewPositionTracker(logger *slog.Logger) *PositionTracker {
	return &PositionTracker{
		positions: make(position.Positions, 0),
		logger:    logger,
	}
}

func (pt *PositionTracker) ApplyExecution(sniperID string, symbolCode string, exec order.Execution, action order.Action, parentOrder *order.Order, recordPnL func(float64)) {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	isExit := false
	exchange := order.EXCHANGE_TOSHO
	tradeType := order.TRADE_TYPE_GENERAL_DAY
	accountType := order.ACCOUNT_SPECIAL

	if parentOrder != nil {
		isExit = (parentOrder.CashMargin == order.CASH_MARGIN_MARGIN_EXIT)
		if parentOrder.Request != nil {
			exchange = parentOrder.Request.Exchange
			tradeType = parentOrder.Request.MarginTradeType
			accountType = parentOrder.Request.AccountType
		}
	}

	if !isExit {
		pt.positions = append(pt.positions, position.Position{
			ExecutionID: exec.ID,
			Symbol:      symbolCode,
			Exchange:    exchange,
			Action:      action,
			TradeType:   tradeType,
			AccountType: accountType,
			LeavesQty:   exec.Qty,
			Price:       exec.Price,
			Meta:        position.PositionMeta{EntryTime: exec.ExecutionTime},
		})
		if pt.logger != nil {
			pt.logger.Info("FILLED",
				slog.String("sniper", sniperID),
				slog.String("symbol", symbolCode),
				slog.String("action", string(action)),
				slog.Float64("qty", exec.Qty),
				slog.Float64("price", exec.Price),
				slog.String("exit_reason", func() string {
					if parentOrder != nil {
						return parentOrder.Reason
					}
					return ""
				}()),
			)
		}
	} else {
		var closePositions []order.ClosePosition
		reason := ""
		if parentOrder != nil {
			if parentOrder.Request != nil {
				closePositions = parentOrder.Request.ClosePositions
			}
			reason = parentOrder.Reason
		}
		pt.reducePositions(sniperID, symbolCode, exec.Qty, exec.Price, exec.ExecutionTime, closePositions, reason, recordPnL)
	}
}

func (pt *PositionTracker) reducePositions(
	sniperID string,
	symbolCode string,
	sellQty float64,
	sellPrice float64,
	sellTime time.Time,
	closePositions []order.ClosePosition,
	closeReason string,
	recordPnL func(float64),
) {
	// Domain Logic Delegation (FIFO matching and reduction)
	totalTradePnL, earliestEntryTime := pt.positions.Reduce(sellQty, sellPrice, closePositions, recordPnL)

	holdTimeSec := 0.0
	if !earliestEntryTime.IsZero() && !sellTime.IsZero() {
		holdTimeSec = sellTime.Sub(earliestEntryTime).Seconds()
	}
	if pt.logger != nil {
		pt.logger.Info("POSITION_CLOSED",
			slog.String("sniper", sniperID),
			slog.String("symbol", symbolCode),
			slog.Float64("pnl", totalTradePnL),
			slog.Float64("hold_time_sec", holdTimeSec),
			slog.String("exit_reason", closeReason),
			slog.Time("entry_time", earliestEntryTime),
			slog.Time("exit_time", sellTime),
		)
	}
}

// HoldQty returns the total physical holding quantity of this symbol pool (positive for long, negative for short).
func (pt *PositionTracker) HoldQty(_ ...string) float64 {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	return pt.positions.TotalHoldQty()
}

func (pt *PositionTracker) GetUnrealizedPnL(_ string, currentPrice float64) float64 {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	return pt.positions.CalculateUnrealizedPnL(currentPrice)
}

func (pt *PositionTracker) LockPositions(_ string, orderID string, closePositions []order.ClosePosition) error {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	for _, cp := range closePositions {
		if err := pt.positions.Lock(orderID, cp.HoldID, cp.Qty); err != nil {
			if pt.logger != nil {
				pt.logger.Warn("⚠️ [PositionTracker] ロック対象の建玉のロックに失敗しました",
					slog.String("orderID", orderID),
					slog.String("holdID", cp.HoldID),
					slog.String("error", err.Error()),
				)
			}
			return err
		}
	}
	return nil
}

func (pt *PositionTracker) UnlockPositions(_ string, orderID string) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	pt.positions.Unlock(orderID)
}

func (pt *PositionTracker) MatchPositionsToClose(_ string, action order.Action, qty float64) ([]order.ClosePosition, order.ClosePositionOrder) {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	return pt.positions.MatchToClose(action, qty)
}

func (pt *PositionTracker) GetCopy(_ ...string) []position.Position {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	posCopy := make([]position.Position, len(pt.positions))
	copy(posCopy, pt.positions)
	return posCopy
}

// RemovePosition removes a specific position by holdID from the symbol pool (used for reconciliation on definitive reject).
func (pt *PositionTracker) RemovePosition(_ string, holdID string) {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	var newPositions []position.Position
	for _, p := range pt.positions {
		if p.ExecutionID == holdID {
			if pt.logger != nil {
				pt.logger.Warn("❌ [PositionTracker] 取引所拒絶によりメモリ上の建玉を強制抹消しました",
					slog.String("holdID", holdID),
					slog.Float64("qty", p.LeavesQty),
				)
			}
			continue
		}
		newPositions = append(newPositions, p)
	}
	pt.positions = newPositions
}
