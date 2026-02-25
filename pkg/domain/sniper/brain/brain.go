package brain

import (
	"fmt"
	"trading-bot/pkg/domain/market"
)

type Action string

const (
	ACTION_BUY  Action = "BUY"
	ACTION_SELL Action = "SELL"
	ACTION_HOLD Action = "HOLD"
)

// Signal は戦略がスナイパーに返す「命令」です
type Signal struct {
	Action    Action
	Quantity  float64
	Price     float64
	OrderType market.OrderType
}

func (a Action) ToMarketAction() (market.Action, error) {
	switch a {
	case ACTION_BUY:
		return market.ACTION_BUY, nil
	case ACTION_SELL:
		return market.ACTION_SELL, nil
	}
	return "", fmt.Errorf("変換できないアクションタイプ")
}
