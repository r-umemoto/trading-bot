package market

import "fmt"

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
	Exchange     ExchangeMarket
}

func (s Symbol) String() string {
	return fmt.Sprintf("%s (%s)", s.Name, s.Code)
}
