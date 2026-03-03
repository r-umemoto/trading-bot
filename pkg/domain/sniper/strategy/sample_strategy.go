package strategy

import (
	"fmt"

	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/brain"
)

// SampleStrategy はデータプールから直接指標を取得する戦略のサンプルです
type SampleStrategy struct{}

func NewSampleStrategy() Strategy {
	return &SampleStrategy{}
}

func (s *SampleStrategy) Evaluate(input StrategyInput) brain.Signal {
	// データプールから自銘柄の最新情報を取得
	state := input.DataPool.GetState(input.Symbol)
	tick := state.LatestTick

	// 必要な指標を、計算処理を気にすることなく DataPool からオンデマンドで引き出す
	sigma := input.DataPool.GetSigma(input.Symbol)
	vwap := input.DataPool.GetVWAP(input.Symbol)

	fmt.Printf(" input: %+v tick: %+v sigma: %.2f vwap: %.2f\n", input, tick, sigma, vwap)
	return brain.Signal{
		Action: brain.ACTION_HOLD,
	}
}

func init() {
	Register("sample", NewSampleStrategy())
}
