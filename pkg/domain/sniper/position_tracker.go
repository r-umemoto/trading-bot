package sniper

import (
	"log/slog"
	"sync"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/position"
)

// PositionTracker tracks physical positions, close matching, and PnL.
// It acts as a thread-safe repository and domain service delegating core logic to position.Positions.
type PositionTracker struct {
	mu        sync.RWMutex
	positions map[string]position.Positions
	logger    *slog.Logger
}

func NewPositionTracker(logger *slog.Logger) *PositionTracker {
	return &PositionTracker{
		positions: make(map[string]position.Positions),
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
		pt.positions[sniperID] = append(pt.positions[sniperID], position.Position{
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
	ps := pt.positions[sniperID]

	// Domain Logic Delegation
	totalTradePnL, earliestEntryTime := ps.Reduce(sellQty, sellPrice, closePositions, recordPnL)
	pt.positions[sniperID] = ps

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

func (pt *PositionTracker) HoldQty(sniperID string) float64 {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	return pt.positions[sniperID].TotalHoldQty()
}

func (pt *PositionTracker) GetUnrealizedPnL(sniperID string, currentPrice float64) float64 {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	return pt.positions[sniperID].CalculateUnrealizedPnL(currentPrice)
}

func (pt *PositionTracker) LockPositions(sniperID string, orderID string, closePositions []order.ClosePosition) error {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	ps := pt.positions[sniperID]
	for _, cp := range closePositions {
		if err := ps.Lock(orderID, cp.HoldID, cp.Qty); err != nil {
			if pt.logger != nil {
				pt.logger.Warn("⚠️ [PositionTracker] ロック対象の建玉のロックに失敗しました",
					slog.String("sniper", sniperID),
					slog.String("orderID", orderID),
					slog.String("holdID", cp.HoldID),
					slog.String("error", err.Error()),
				)
			}
			return err
		}
	}
	pt.positions[sniperID] = ps
	return nil
}

func (pt *PositionTracker) UnlockPositions(sniperID string, orderID string) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	pt.positions[sniperID].Unlock(orderID)
}

func (pt *PositionTracker) MatchPositionsToClose(sniperID string, action order.Action, qty float64) ([]order.ClosePosition, order.ClosePositionOrder) {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	return pt.positions[sniperID].MatchToClose(action, qty)
}

func (pt *PositionTracker) GetCopy(sniperID string) []position.Position {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	pos := pt.positions[sniperID]
	posCopy := make([]position.Position, len(pos))
	copy(posCopy, pos)
	return posCopy
}

// RemovePosition は特定の建玉を PositionTracker から強制削除します（不整合発生時の自己復旧用）
func (pt *PositionTracker) RemovePosition(sniperID string, holdID string) {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	positions := pt.positions[sniperID]
	var newPositions []position.Position
	for _, p := range positions {
		if p.ExecutionID == holdID {
			if pt.logger != nil {
				pt.logger.Warn("❌ [PositionTracker] 取引所拒絶によりメモリ上の建玉を強制抹消しました",
					slog.String("sniper", sniperID),
					slog.String("holdID", holdID),
					slog.Float64("qty", p.LeavesQty),
				)
			}
			continue
		}
		newPositions = append(newPositions, p)
	}
	pt.positions[sniperID] = newPositions
}
