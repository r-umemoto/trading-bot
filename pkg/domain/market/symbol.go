package market

import "fmt"

// SymbolDetail は銘柄の基本属性を保持するエンティティです
type SymbolDetail struct {
	Symbol          string
	SymbolName      string
	PriceRangeGroup PriceRangeGroup
}

// WatchTarget は監視対象の設定を保持するバリューオブジェクトです
type WatchTarget struct {
	Detail       SymbolDetail
	StrategyName string
	Exchange     ExchangeMarket
}

func (s SymbolDetail) String() string {
	return fmt.Sprintf("%s (%s)", s.SymbolName, s.Symbol)
}
