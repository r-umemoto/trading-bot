package strategy

import "github.com/r-umemoto/trading-bot/pkg/domain/market"

type SimpleStrategyFactory struct {
}

func (f *SimpleStrategyFactory) NewStrategy(symbol string, dataPool market.DataPool) Strategy {
	oneMinBar := dataPool.GetOrCreateIndicator(symbol, "1min_bar", func() market.Indicator {
		return market.NewOneMinBarIndicator("1min_bar")
	}).(*market.OneMinBarIndicator)

	return NewSampleStrategy(oneMinBar)
}

func init() {
	Register("sample", &SimpleStrategyFactory{})
}
