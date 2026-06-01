package brain

import (
	"fmt"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
)

type Action string

const (
	ACTION_BUY  Action = "BUY"
	ACTION_SELL Action = "SELL"
	ACTION_HOLD Action = "HOLD"
)

type TradeType int

const (
	TradeEntry TradeType = iota // 新規建て
	TradeExit                   // 返済決済
)

// Signal は戦略がスナイパーに返す「命令」です
type Signal struct {
	Action    Action
	TradeType TradeType // 🌟 新規か決済か
	Quantity  float64
	Price     float64
	OrderType order.OrderType
	Reason    string // 🌟 命令の理由 (分析用)
}

func (a Action) ToMarketAction() (order.Action, error) {
	switch a {
	case ACTION_BUY:
		return order.ACTION_BUY, nil
	case ACTION_SELL:
		return order.ACTION_SELL, nil
	}
	return "", fmt.Errorf("変換できないアクションタイプ")
}
