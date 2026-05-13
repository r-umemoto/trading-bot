package brain

import (
	"fmt"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
)

type Action string

const (
	ACTION_BUY  Action = "BUY"
	ACTION_SELL Action = "SELL"
	ACTION_HOLD Action = "HOLD"
)

// Signal は戦略がスナイパーに返す「命令」です
type Signal struct {
	Action       Action
	Quantity     float64
	Price        float64
	OrderType    market.OrderType
	Reason       string           // 🌟 命令の理由 (分析用)
	HasIFD       bool             // IFD注文を伴うか
	IFDAction    Action           // IFD注文のアクション (BUY/SELL)
	IFDPrice     float64          // IFD注文の価格
	IFDOrderType market.OrderType // IFD注文の執行条件
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
