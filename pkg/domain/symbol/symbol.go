package symbol

import (
	"fmt"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
)

// Symbol は銘柄の基本属性を保持するエンティティです
type Symbol struct {
	Code            string
	Name            string
	PriceRangeGroup PriceRangeGroup
}

// WatchTarget は監視対象の設定を保持するバリューオブジェクトです
type WatchTarget struct {
	Detail       Symbol
	StrategyName string
	Exchange     order.ExchangeMarket
	Params       interface{}
}

func (s Symbol) String() string {
	return fmt.Sprintf("%s (%s)", s.Name, s.Code)
}
