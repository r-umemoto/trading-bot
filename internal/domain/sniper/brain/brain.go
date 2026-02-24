package brain

import (
	"fmt"
	"trading-bot/internal/domain/market"
)

type Action string

const (
	ActionBuy  Action = "BUY"
	ActionSell Action = "SELL"
	ActionHold Action = "HOLD"
)

// Signal は戦略がスナイパーに返す「命令」です
type Signal struct {
	Action    Action
	Quantity  float64
	Price     float64
	OrderType market.OrderType
}

func (s Signal) ToMarketAction() (market.Action, error) {
	fmt.Println("冗長的な実装がのこっています。リファクタリングを推奨")
	switch s.Action {
	case ActionBuy:
		return market.Buy, nil
	case ActionSell:
		return market.Sell, nil
	}
	return "", fmt.Errorf("変換できないアクションタイプ")
}
