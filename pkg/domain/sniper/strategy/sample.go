package strategy

import (
	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/brain"
)

// StrategyState は、戦略が銘柄ごとに保持したい固有のステートを表します
type StrategyState struct {
	count float64
}

// SampleStrategy はデータプールから直接指標を取得する戦略のサンプルです
type SampleStrategy struct {
	name      string
	state     StrategyState
	oneMinBar *market.OneMinBarIndicator
	highPrice float64
}

func (s *SampleStrategy) Name() string {
	return s.name
}

// Evaluate is purely functional
func (s *SampleStrategy) Evaluate(input StrategyInput) brain.Signal {

	if !input.LatestTick.IsExecution() {
		return brain.Signal{
			Action: brain.ACTION_HOLD,
		}
	}

	if input.HoldQty > 0 {
		curretPrice := input.LatestTick.Price
		if s.highPrice == 0 {
			s.highPrice = curretPrice
		}
		if curretPrice < s.highPrice*0.80 && input.AveragePrice > curretPrice {
			s.highPrice = 0
			return brain.Signal{
				Action:   brain.ACTION_SELL,
				Quantity: input.HoldQty,
			}
		}
		if curretPrice < input.AveragePrice*0.997 {
			s.highPrice = 0
			return brain.Signal{
				Action:   brain.ACTION_SELL,
				Quantity: input.HoldQty,
			}
		}
		if curretPrice > s.highPrice {
			s.highPrice = curretPrice
		}
		return brain.Signal{
			Action: brain.ACTION_HOLD,
		}
	}

	// 1分足の終値が3回連続で上昇したら買い
	bars := s.oneMinBar.Bars()
	if len(bars) < 3 {
		return brain.Signal{
			Action: brain.ACTION_HOLD,
		}
	}

	// 過去3本のバーの終値を取得
	bar1 := bars[len(bars)-3]
	bar2 := bars[len(bars)-2]
	bar3 := bars[len(bars)-1]

	// 終値が3回連続で上昇しているかチェック
	if bar1.Close < bar2.Close && bar2.Close < bar3.Close {
		return brain.Signal{
			Action:   brain.ACTION_BUY,
			Quantity: 100,
		}
	}

	return brain.Signal{Action: brain.ACTION_HOLD}
}

// ----------------------------------------------------------------------------
// Factory & Registration
// ----------------------------------------------------------------------------

type SimpleStrategyFactory struct{}

func (f *SimpleStrategyFactory) NewStrategy(symbol string, dataPool market.DataPool) Strategy {
	// Sample戦略が必要とするインジケーター（1分足）をDataPoolに要求・生成する
	oneMinBar := dataPool.GetOrCreateIndicator(symbol, "1min_bar", func() market.Indicator {
		return market.NewOneMinBarIndicator("1min_bar")
	}).(*market.OneMinBarIndicator)

	return &SampleStrategy{
		name: "sample",
		state: StrategyState{
			count: 0,
		},
		oneMinBar: oneMinBar,
	}
}

func init() {
	// "sample" という名前でこのファクトリをシステム全体に登録する
	Register("sample", &SimpleStrategyFactory{})
}
