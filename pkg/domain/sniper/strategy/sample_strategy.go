package strategy

import (
	"fmt"

	"github.com/r-umemoto/trading-bot/pkg/domain/market/calculator"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/brain"
)

// SampleStrategy は指標を内部で計算する戦略のサンプルです
type SampleStrategy struct {
	sigmaCalc *calculator.SigmaCalculator
}

func NewSampleStrategy() Strategy {
	return &SampleStrategy{}
}

func (s *SampleStrategy) Evaluate(input StrategyInput) brain.Signal {
	// 戦略側で指標を再計算（必要ならば）
	if s.sigmaCalc == nil {
		s.sigmaCalc = calculator.NewSigmaCalculator(input.LatestTick.TradingVolume)
	} else {
		// 計算を実行（サンプルとして呼び出しているだけ）
		_, _ = s.sigmaCalc.UpdateAndGetMetrics(input.LatestTick.TradingVolume, input.LatestTick.Price)
	}
	fmt.Printf(" input: %+v \n", input)
	return brain.Signal{
		Action: brain.ACTION_HOLD,
	}
}

func init() {
	Register("sample", NewSampleStrategy())
}
