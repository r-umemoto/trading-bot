package report

import (
	"context"
	"time"
)

type StrategyDetail struct {
	Name          string  `json:"name" firestore:"name"`
	Trades        int     `json:"trades" firestore:"trades"`
	Wins          int     `json:"wins" firestore:"wins"`
	Losses        int     `json:"losses" firestore:"losses"`
	RealizedPnL   float64 `json:"realized_pnl" firestore:"realized_pnl"`
	UnrealizedPnL float64 `json:"unrealized_pnl" firestore:"unrealized_pnl"`
}

type DailyReport struct {
	Date          string           `json:"date" firestore:"date"`                   // "YYYY-MM-DD"
	UpdatedAt     time.Time        `json:"updated_at" firestore:"updated_at"`       // 更新日時
	Trades        int              `json:"trades" firestore:"trades"`               // 取引回数
	Wins          int              `json:"wins" firestore:"wins"`                   // 勝ち数
	Losses        int              `json:"losses" firestore:"losses"`               // 敗け数
	WinRate       float64          `json:"win_rate" firestore:"win_rate"`           // 勝率 (%)
	RealizedPnL   float64          `json:"realized_pnl" firestore:"realized_pnl"`   // 実現損益
	UnrealizedPnL float64          `json:"unrealized_pnl" firestore:"unrealized_pnl"` // 含み損益
	TotalPnL      float64          `json:"total_pnl" firestore:"total_pnl"`         // 合計損益
	Details       []StrategyDetail `json:"details" firestore:"details"`             // ストラテジー・銘柄別の内訳
}

type Repository interface {
	Save(ctx context.Context, r *DailyReport) error
}
