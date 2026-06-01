package strategy

import (
	"log/slog"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/brain"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

// StrategyState は、戦略が銘柄ごとに保持したい固有のステートを表します
type StrategyState struct {
	count float64
}

// SampleStrategy はデータプールから直接指標を取得する戦略のサンプルです
type SampleStrategy struct {
	name      string
	state     StrategyState
	oneMinBar *tick.OneMinBarIndicator
	highPrice float64
}

func (s *SampleStrategy) Name() string {
	return s.name
}

func (s *SampleStrategy) AnalysisLogger() *slog.Logger {
	return nil
}

// Evaluate is purely functional
func (s *SampleStrategy) Evaluate(input StrategyInput) brain.Signal {
	holdQty := input.HoldQty()
	avgPrice := input.AveragePrice()

	if !input.LatestTick.IsExecution() {
		return brain.Signal{
			Action: brain.ACTION_HOLD,
		}
	}

	if holdQty > 0 {
		curretPrice := input.LatestTick.Price
		if s.highPrice == 0 {
			s.highPrice = curretPrice
		}
		if curretPrice < s.highPrice*0.80 && avgPrice > curretPrice {
			s.highPrice = 0
			return brain.Signal{
				Action:    brain.ACTION_SELL,
				TradeType: brain.TradeExit,
				Quantity:  holdQty,
			}
		}
		if curretPrice < avgPrice*0.997 {
			s.highPrice = 0
			return brain.Signal{
				Action:    brain.ACTION_SELL,
				TradeType: brain.TradeExit,
				Quantity:  holdQty,
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
			Action:    brain.ACTION_BUY,
			TradeType: brain.TradeEntry,
			Quantity:  100,
		}
	}

	return brain.Signal{Action: brain.ACTION_HOLD}
}

func (s *SampleStrategy) IfDone(input StrategyInput, prevSignal brain.Signal) brain.Signal {
	return brain.Signal{Action: brain.ACTION_HOLD}
}

func (s *SampleStrategy) ShouldCancel(input StrategyInput, ord *order.Order) bool {
	return false
}

// ----------------------------------------------------------------------------
// Factory & Registration
// ----------------------------------------------------------------------------

type SimpleStrategyFactory struct{}

func (f *SimpleStrategyFactory) NewStrategy(detail symbol.Symbol, dataPool tick.DataPool, params interface{}) Strategy {
	// Sample戦略が必要とするインジケーター（1分足）をDataPoolに要求・生成する
	oneMinBar := dataPool.GetOrCreateIndicator(detail.Code, "1min_bar", func() tick.Indicator {
		return tick.NewOneMinBarIndicator("1min_bar")
	}).(*tick.OneMinBarIndicator)

	return &SampleStrategy{
		name: "sample",
		state: StrategyState{
			count: 0,
		},
		oneMinBar: oneMinBar,
	}
}

func (f *SimpleStrategyFactory) CreateExecutionPolicy(params interface{}) ExecutionPolicy {
	return &NoopPolicy{}
}

func init() {
	// "sample" という名前でこのファクトリをシステム全体に登録する
	Register("sample", &SimpleStrategyFactory{})
}
