package strategy

import (
	"fmt"
	"github.com/r-umemoto/trading-bot/pkg/domain/market"
)

type StrategyFactory interface {
	NewStrategy(detail market.Symbol, dataPool market.DataPool) Strategy
}

var registry = make(map[string]StrategyFactory)

// Register a strategy with a given name.
func Register(name string, s StrategyFactory) {
	registry[name] = s
}

// Get a strategy by its name.
func GetFactory(name string) (StrategyFactory, error) {
	s, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("strategy not found: %s", name)
	}
	return s, nil
}
