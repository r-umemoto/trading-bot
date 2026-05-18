package feed

import (
	"fmt"
	"time"

	"github.com/piquette/finance-go/chart"
	"github.com/piquette/finance-go/datetime"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

// YahooFinanceFeeder は Yahoo Finance からヒストリカルデータを取得し、システムに供給します
type YahooFinanceFeeder struct {
	symbol   string
	interval datetime.Interval
}

// NewYahooFinanceFeeder は新しい YahooFinanceFeeder を作成します
func NewYahooFinanceFeeder(symbol string, interval datetime.Interval) *YahooFinanceFeeder {
	return &YahooFinanceFeeder{
		symbol:   symbol,
		interval: interval,
	}
}

// FetchSMA は過去のデータからチャートからSMA(単純移動平均)を計算して返します
func (f *YahooFinanceFeeder) FetchSMA(period int) (float64, error) {
	// 指定期間のデータを確実に取得するために、指定された SMA 期間 period に応じて動的に日数を算出します。
	// 土日祝日や連休を考慮し、余裕を持って period * 2 日前を指定します（最低でも過去10日間）。
	end := time.Now()
	daysBack := period * 2
	if daysBack < 10 {
		daysBack = 10
	}
	start := end.AddDate(0, 0, -daysBack)

	p := &chart.Params{
		Symbol:   f.symbol,
		Start:    datetime.New(&start),
		End:      datetime.New(&end),
		Interval: f.interval,
	}

	iter := chart.Get(p)
	var prices []float64
	for iter.Next() {
		bar := iter.Bar()
		price, _ := bar.Close.Float64()
		prices = append(prices, price)
	}

	if err := iter.Err(); err != nil {
		return 0, fmt.Errorf("failed to fetch chart data for SMA: %w", err)
	}

	if len(prices) < period {
		return 0, fmt.Errorf("insufficient data for SMA: got %d bars, need %d", len(prices), period)
	}

	// 直近 n 本の平均を計算
	var sum float64
	for i := len(prices) - period; i < len(prices); i++ {
		sum += prices[i]
	}

	return sum / float64(period), nil
}

// YahooFinanceFeederProvider は tick.HistoricalFeederProvider インターフェースの本番用実装です
type YahooFinanceFeederProvider struct {
	interval datetime.Interval
}

var _ tick.HistoricalFeederProvider = (*YahooFinanceFeederProvider)(nil)

func NewYahooFinanceFeederProvider(interval datetime.Interval) *YahooFinanceFeederProvider {
	return &YahooFinanceFeederProvider{
		interval: interval,
	}
}

// GetFeeder は指定された symbol に対応する YahooFinanceFeeder を生成し、tick.HistoricalFeeder として返します
func (p *YahooFinanceFeederProvider) GetFeeder(symbol string) tick.HistoricalFeeder {
	return NewYahooFinanceFeeder(symbol, p.interval)
}
