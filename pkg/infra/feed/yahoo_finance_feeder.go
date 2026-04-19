package feed

import (
	"fmt"
	"time"

	"github.com/piquette/finance-go/chart"
	"github.com/piquette/finance-go/datetime"
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
	// 指定期間のデータを確実に取得するために、過去10日分を取得します
	// (日本の連休や祝日が重なっても10日あればSMA75程度までは確実にカバー可能)
	end := time.Now()
	start := end.AddDate(0, 0, -10)

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
