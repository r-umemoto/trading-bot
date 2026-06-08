package sniper

import (
	"log/slog"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/position"
)

// PositionTracker tracks physical positions, close matching, and PnL.
type PositionTracker struct {
	positions map[string][]position.Position
	logger    *slog.Logger
}

func NewPositionTracker(logger *slog.Logger) *PositionTracker {
	return &PositionTracker{
		positions: make(map[string][]position.Position),
		logger:    logger,
	}
}

func (pt *PositionTracker) ApplyExecution(sniperID string, symbolCode string, exec order.Execution, action order.Action, parentOrder *order.Order, recordPnL func(float64)) {
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

func (pt *PositionTracker) reducePositions(sniperID string, symbolCode string, sellQty float64, sellPrice float64, sellTime time.Time, closePositions []order.ClosePosition, closeReason string, recordPnL func(float64)) {
	remainingToSell := sellQty
	var totalTradePnL float64
	var earliestEntryTime time.Time

	positions := pt.positions[sniperID]

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
				if closeQty > qtyToClose {
					closeQty = qtyToClose
				}
				if closeQty > remainingToSell {
					closeQty = remainingToSell
				}

				if earliestEntryTime.IsZero() || (!p.Meta.EntryTime.IsZero() && p.Meta.EntryTime.Before(earliestEntryTime)) {
					earliestEntryTime = p.Meta.EntryTime
				}

				pnlFactor := 1.0
				if p.Action == order.ACTION_SELL {
					pnlFactor = -1.0
				}
				tradePnL := (sellPrice - p.Price) * closeQty * pnlFactor
				totalTradePnL += tradePnL
				recordPnL(tradePnL)

				p.LeavesQty -= closeQty
				closeMap[p.ExecutionID] -= closeQty
				remainingToSell -= closeQty

				if p.LeavesQty > 0 {
					newPositions = append(newPositions, p)
				}
			} else {
				newPositions = append(newPositions, p)
			}
		}
		positions = newPositions
	}

	if remainingToSell > 0 {
		var newPositions []position.Position
		for _, p := range positions {
			if remainingToSell <= 0 {
				newPositions = append(newPositions, p)
				continue
			}

			closeQty := p.LeavesQty
			if closeQty > remainingToSell {
				closeQty = remainingToSell
			}

			if earliestEntryTime.IsZero() || (!p.Meta.EntryTime.IsZero() && p.Meta.EntryTime.Before(earliestEntryTime)) {
				earliestEntryTime = p.Meta.EntryTime
			}

			pnlFactor := 1.0
			if p.Action == order.ACTION_SELL {
				pnlFactor = -1.0
			}
			tradePnL := (sellPrice - p.Price) * closeQty * pnlFactor
			totalTradePnL += tradePnL
			recordPnL(tradePnL)

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
	pt.positions[sniperID] = positions

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
	var total float64
	for _, p := range pt.positions[sniperID] {
		if p.Action == order.ACTION_SELL {
			total -= p.LeavesQty
		} else {
			total += p.LeavesQty
		}
	}
	return total
}

func (pt *PositionTracker) GetUnrealizedPnL(sniperID string, currentPrice float64) float64 {
	var unrealized float64
	for _, p := range pt.positions[sniperID] {
		pnlFactor := 1.0
		if p.Action == order.ACTION_SELL {
			pnlFactor = -1.0
		}
		unrealized += (currentPrice - p.Price) * p.LeavesQty * pnlFactor
	}
	return unrealized
}

func (pt *PositionTracker) MatchPositionsToClose(sniperID string, action order.Action, qty float64, lockedHoldIDs map[string]bool) ([]order.ClosePosition, order.ClosePositionOrder) {
	var closePositions []order.ClosePosition
	remainingQty := qty

	targetAction := order.ACTION_BUY
	if action == order.ACTION_BUY {
		targetAction = order.ACTION_SELL
	}

	for _, p := range pt.positions[sniperID] {
		if p.Action != targetAction {
			continue
		}
		if lockedHoldIDs[p.ExecutionID] {
			continue
		}
		if remainingQty <= 0 {
			break
		}
		closeQty := p.LeavesQty
		if closeQty > remainingQty {
			closeQty = remainingQty
		}
		closePositions = append(closePositions, order.ClosePosition{HoldID: p.ExecutionID, Qty: closeQty})
		remainingQty -= closeQty
	}
	return closePositions, order.CLOSE_POSITION_ORDER_NONE
}

func (pt *PositionTracker) GetCopy(sniperID string) []position.Position {
	pos := pt.positions[sniperID]
	posCopy := make([]position.Position, len(pos))
	copy(posCopy, pos)
	return posCopy
}
