package position

import (
	"fmt"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
)

// Positions は複数の建玉をまとめたファーストクラスコレクションです
type Positions []Position

// Lock は指定された注文IDによって数量をロック（予約）します
func (ps Positions) Lock(orderID string, holdID string, qty float64) error {
	for i := range ps {
		if ps[i].ExecutionID == holdID {
			return ps[i].Lock(orderID, qty)
		}
	}
	return fmt.Errorf("position not found for holdID: %s", holdID)
}

// Unlock は指定された注文IDに紐づくロックを解除します
func (ps Positions) Unlock(orderID string) {
	for i := range ps {
		ps[i].Unlock(orderID)
	}
}

// MatchToClose は指定した方向と数量に一致する返済候補の建玉を探します
func (ps Positions) MatchToClose(action order.Action, qty float64) ([]order.ClosePosition, order.ClosePositionOrder) {
	var closePositions []order.ClosePosition
	remainingQty := qty

	targetAction := order.ACTION_BUY
	if action == order.ACTION_BUY {
		targetAction = order.ACTION_SELL
	}

	for _, p := range ps {
		if p.Action != targetAction {
			continue
		}
		available := p.AvailableQty()
		if available <= 0 {
			continue
		}
		if remainingQty <= 0 {
			break
		}
		closeQty := available
		if closeQty > remainingQty {
			closeQty = remainingQty
		}
		closePositions = append(closePositions, order.ClosePosition{HoldID: p.ExecutionID, Qty: closeQty})
		remainingQty -= closeQty
	}
	return closePositions, order.CLOSE_POSITION_ORDER_NONE
}

// CalculateUnrealizedPnL は評価損益を計算します
func (ps Positions) CalculateUnrealizedPnL(currentPrice float64) float64 {
	var unrealized float64
	for _, p := range ps {
		pnlFactor := 1.0
		if p.Action == order.ACTION_SELL {
			pnlFactor = -1.0
		}
		unrealized += (currentPrice - p.Price) * p.LeavesQty * pnlFactor
	}
	return unrealized
}

// TotalHoldQty は現在の純保有数量（ロングはプラス、ショートはマイナス）を計算します
func (ps Positions) TotalHoldQty() float64 {
	var total float64
	for _, p := range ps {
		if p.Action == order.ACTION_SELL {
			total -= p.LeavesQty
		} else {
			total += p.LeavesQty
		}
	}
	return total
}

// Reduce は約定を適用し、残った新しい Positions スライスと、発生した実現損益 (PnL) および最古の建玉のエントリー時刻を返します
func (ps *Positions) Reduce(
	sellQty float64,
	sellPrice float64,
	closePositions []order.ClosePosition,
	recordPnL func(float64),
) (float64, time.Time) {
	remainingToSell := sellQty
	var totalTradePnL float64
	var earliestEntryTime time.Time

	current := *ps

	if len(closePositions) > 0 {
		closeMap := make(map[string]float64)
		for _, cp := range closePositions {
			closeMap[cp.HoldID] = cp.Qty
		}

		var newPositions []Position
		for _, p := range current {
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
		current = newPositions
		remainingToSell = 0
	}

	if remainingToSell > 0 {
		var newPositions []Position
		for _, p := range current {
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
		current = newPositions
	}

	*ps = current
	return totalTradePnL, earliestEntryTime
}
