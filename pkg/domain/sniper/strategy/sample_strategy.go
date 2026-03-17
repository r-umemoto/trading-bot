package strategy

import (
	"fmt"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/brain"
)

// StrategyState は、戦略が銘柄ごとに保持したい固有のステートを表します
type StrategyState struct {
	count float64
}

// SampleStrategy はデータプールから直接指標を取得する戦略のサンプルです
type SampleStrategy struct {
	state StrategyState
}

func NewSampleStrategy() Strategy {
	return &SampleStrategy{
		state: StrategyState{
			count: 0,
		},
	}
}

func (s *SampleStrategy) Evaluate(input StrategyInput) brain.Signal {
	// データプールから自銘柄の最新情報を取得
	state := input.DataPool.GetState(input.Symbol)
	tick := state.LatestTick

	// 必要な指標を、計算処理を気にすることなく DataPool からオンデマンドで引き出す
	sigma := input.DataPool.GetSigma(input.Symbol)
	vwap := input.DataPool.GetVWAP(input.Symbol)

	// --- 5分足VWAP（定율1%）を用いたトレンド判定 ---
	summaries := input.DataPool.GetFiveMinSummaries(input.Symbol)
	var prev5MinVWAP float64
	if len(summaries) > 0 {
		// 直近確定した5分間のVWAP
		prev5MinVWAP = summaries[len(summaries)-1].VWAP
	}
	// 蓄積中の5分間のリアルタイムなVWAP
	curr5MinVWAP := input.DataPool.GetCurrentFiveMinVWAP(input.Symbol)

	trend := "neutral"

	// 現在の時間枠の開始時刻からどれくらい経過しているかを判定し、開始直後はスキップする
	currentWindowStart := tick.CurrentPriceTime.Truncate(5 * time.Minute)
	elapsedSinceWindowStart := tick.CurrentPriceTime.Sub(currentWindowStart)

	// 最低でも1分(60秒)経過していないと、VWAPの精度が低いため判定を見送る
	isReliable := elapsedSinceWindowStart >= 1*time.Minute

	if isReliable && prev5MinVWAP > 0 && curr5MinVWAP > 0 {
		threshold := prev5MinVWAP * 0.01 // 1%の閾値
		upperBound := prev5MinVWAP + threshold
		lowerBound := prev5MinVWAP - threshold

		if curr5MinVWAP > upperBound {
			trend = "upward"
		} else if curr5MinVWAP < lowerBound {
			trend = "downward"
		}
	}

	fmt.Printf(" input: %+v tick: %+v sigma: %.2f vwap: %.2f\n", input, tick, sigma, vwap)
	if isReliable {
		fmt.Printf(" [5-Min VWAP Trend (1%%)] prev: %.2f / curr: %.2f => trend: %s\n", prev5MinVWAP, curr5MinVWAP, trend)
	} else {
		fmt.Printf(" [5-Min VWAP Trend (1%%)] Waiting for more data... elapsed: %v\n", elapsedSinceWindowStart)
	}

	// トレンドに応じたアクションの設定例（シンプル化のためログ出力とアクション仮設定のみ）
	var action brain.Action
	switch trend {
	case "upward":
		action = brain.ACTION_BUY
	case "downward":
		action = brain.ACTION_SELL
	default:
		action = brain.ACTION_HOLD
	}

	return brain.Signal{
		Action: action,
	}
}
