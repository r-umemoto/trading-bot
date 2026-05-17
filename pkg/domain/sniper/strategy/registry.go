package strategy

import (
	"fmt"

	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

type StrategyFactory interface {
	NewStrategy(detail symbol.Symbol, dataPool tick.DataPool, params interface{}) Strategy
	CreateExecutionPolicy(params interface{}) ExecutionPolicy
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
