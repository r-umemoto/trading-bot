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
	formattedSymbol := symbol
	if len(symbol) == 4 {
		isNumeric := true
		for _, r := range symbol {
			if r < '0' || r > '9' {
				isNumeric = false
				break
			}
		}
		if isNumeric {
			formattedSymbol = symbol + ".T"
		}
	}

	return &YahooFinanceFeeder{
		symbol:   formattedSymbol,
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

// FetchPreviousClose は前営業日の確定終値を取得して返します。
// 取引時間中に API を叩いた場合、当日の未確定足が最新（最後の足）として返ってくる可能性があるため、
// 起動日のローカル当日日付 (00:00:00) よりタイムスタンプが古い足の中から、最も新しい Close を取得します。
func (f *YahooFinanceFeeder) FetchPreviousClose() (float64, error) {
	end := time.Now()
	// 土日祝日や連休を考慮し、余裕を持って過去5日分を取得します
	start := end.AddDate(0, 0, -5)

	p := &chart.Params{
		Symbol:   f.symbol,
		Start:    datetime.New(&start),
		End:      datetime.New(&end),
		Interval: f.interval,
	}

	iter := chart.Get(p)
	
	// 今日のローカルタイム 00:00:00 を取得
	todayLocal := time.Now().Truncate(24 * time.Hour)

	var lastClose float64
	var found bool

	for iter.Next() {
		bar := iter.Bar()
		
		// piquette/finance-go が返す Timestamp (Unix 時間)
		barTime := time.Unix(int64(bar.Timestamp), 0)
		
		// タイムスタンプが今日のローカル日付 (00:00:00) より前の足の場合、
		// それは確実に確定済みの前営業日以前の終値です
		if barTime.Before(todayLocal) {
			price, _ := bar.Close.Float64()
			lastClose = price
			found = true
		}
	}

	if err := iter.Err(); err != nil {
		return 0, fmt.Errorf("failed to fetch chart data for previous close: %w", err)
	}

	if !found {
		return 0, fmt.Errorf("no historical chart data found for previous close (symbol: %s)", f.symbol)
	}

	return lastClose, nil
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
