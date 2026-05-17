package portfolio

import (
	"context"
	"fmt"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
)

// SymbolTarget defines a symbol to watch, including the multiple strategies to apply and metadata.
type SymbolTarget struct {
	Symbol     string                 `json:"symbol"`
	Exchange   order.ExchangeMarket   `json:"exchange"`
	Strategies []string               `json:"strategies"`
	Sector     string                 `json:"sector"`
	Params     map[string]interface{} `json:"params"`
}

// BuildWatchList flattens a slice of SymbolTarget into a slice of symbol.WatchTarget.
// This allows a single symbol to be mapped to multiple symbol.WatchTarget instances
// when multiple strategies are specified.
func BuildWatchList(ctx context.Context, gateway market.MarketGateway, targets []SymbolTarget) ([]symbol.WatchTarget, error) {
	var watchList []symbol.WatchTarget

	for _, t := range targets {
		// 🌟 APIから詳細情報を取得
		detail, err := gateway.GetSymbol(ctx, t.Symbol, t.Exchange)
		if err != nil {
			return nil, fmt.Errorf("銘柄詳細の取得に失敗しました (%s): %w", t.Symbol, err)
		}

		for _, strategy := range t.Strategies {
			watchList = append(watchList, symbol.WatchTarget{
				Detail:       detail,
				StrategyName: strategy,
				Exchange:     t.Exchange,
				Params:       t.Params[strategy],
			})
		}
	}

	return watchList, nil
}
