package report

import (
	"context"
	"time"
)

type AggregatedPerformance struct {
	Name          string  `json:"name" firestore:"name"`
	Trades        int     `json:"trades" firestore:"trades"`
	Wins          int     `json:"wins" firestore:"wins"`
	Losses        int     `json:"losses" firestore:"losses"`
	Draws         int     `json:"draws" firestore:"draws"`
	WinRate       float64 `json:"win_rate" firestore:"win_rate"`
	RealizedPnL   float64 `json:"realized_pnl" firestore:"realized_pnl"`
	UnrealizedPnL float64 `json:"unrealized_pnl" firestore:"unrealized_pnl"`
	TotalPnL      float64 `json:"total_pnl" firestore:"total_pnl"`
}

type DailyReport struct {
	Date      string                  `json:"date" firestore:"date"`                   // "YYYY-MM-DD"
	UpdatedAt time.Time               `json:"updated_at" firestore:"updated_at"`       // 更新日時
	Total     AggregatedPerformance   `json:"total" firestore:"total"`                 // 全体成績
	Symbols   []AggregatedPerformance `json:"symbols" firestore:"symbols"`             // 銘柄別成績
	Strats    []AggregatedPerformance `json:"strats" firestore:"strats"`               // ストラテジー別成績
	Combined  []AggregatedPerformance `json:"combined" firestore:"combined"`           // 銘柄×ストラテジー成績
}

type Repository interface {
	Save(ctx context.Context, r *DailyReport) error
}
