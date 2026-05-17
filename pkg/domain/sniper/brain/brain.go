package brain

import (
	"fmt"

	"github.com/r-umemoto/trading-bot/pkg/domain/ord"
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
	OrderType ord.OrderType
	Reason    string // 🌟 命令の理由 (分析用)
}

func (a Action) ToMarketAction() (ord.Action, error) {
	switch a {
	case ACTION_BUY:
		return ord.ACTION_BUY, nil
	case ACTION_SELL:
		return ord.ACTION_SELL, nil
	}
	return "", fmt.Errorf("変換できないアクションタイプ")
}
