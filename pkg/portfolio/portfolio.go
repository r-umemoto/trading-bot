package portfolio

import (
	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/engine"
)

// SymbolTarget defines a symbol to watch, including the multiple strategies to apply and metadata.
type SymbolTarget struct {
	Symbol     string                `json:"symbol"`
	Exchange   market.ExchangeMarket `json:"exchange"`
	Strategies []string              `json:"strategies"`
	Sector     string                `json:"sector"`
}

// BuildWatchList flattens a slice of SymbolTarget into a slice of engine.WatchTarget.
// This allows a single symbol to be mapped to multiple engine.WatchTarget instances
// when multiple strategies are specified.
func BuildWatchList(targets []SymbolTarget) []engine.WatchTarget {
	var watchList []engine.WatchTarget

	for _, t := range targets {
		for _, strategy := range t.Strategies {
			watchList = append(watchList, engine.WatchTarget{
				Symbol:       t.Symbol,
				StrategyName: strategy,
				Exchange:     t.Exchange,
			})
		}
	}

	return watchList
}
