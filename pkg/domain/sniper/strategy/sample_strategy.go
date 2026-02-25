package strategy

import "trading-bot/pkg/domain/sniper/brain"

// SampleStrategy is a simple strategy that does nothing.
type SampleStrategy struct{}

func NewSampleStrategy() Strategy {
	return &SampleStrategy{}
}

func (s *SampleStrategy) Evaluate(input StrategyInput) brain.Signal {
	return brain.Signal{
		Action: brain.ACTION_HOLD,
	}
}

func init() {
	Register("sample", NewSampleStrategy())
}
