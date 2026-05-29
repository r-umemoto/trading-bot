package portfolio

import (
	"github.com/r-umemoto/trading-bot/pkg/domain/order"
)

// SymbolTarget は portfolio.json に記述されるマスタ資産（銘柄登録）を表す構造体です。
type SymbolTarget struct {
	Symbol   string               `json:"symbol"`
	Name     string               `json:"name"`
	Exchange order.ExchangeMarket `json:"exchange"`
	Sector   string               `json:"sector"`
	Enabled  bool                 `json:"enabled"`
}
