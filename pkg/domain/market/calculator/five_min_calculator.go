package calculator

import (
	"time"
)

// FiveMinSummary は5分間の出来高とVWAPのサマリ結果を保持します
type FiveMinSummary struct {
	StartTime     time.Time
	EndTime       time.Time
	TradingVolume float64
	VWAP          float64
}

// Tick は集計に必要なTickデータを表すインターフェース、もしくは直接構造体を受け取ります。
// ここでは domain/market パッケージから値を受け取るための構造体を定義（I/F依存を減らすため）するか、
// そのまま参照します。今回は引数で必要な値のみを受け取るようにします。
type FiveMinCalculator struct {
	ticks            []tickData
	fiveMinSummaries []FiveMinSummary
	windowStart      time.Time
	windowStartVol   float64
}

type tickData struct {
	Price            float64
	TradingVolume    float64
	CurrentPriceTime time.Time
}

func NewFiveMinCalculator() *FiveMinCalculator {
	return &FiveMinCalculator{
		ticks:            make([]tickData, 0),
		fiveMinSummaries: make([]FiveMinSummary, 0),
	}
}

// Update は新しいTickデータを受け取り、5分足の集計状態を更新します
func (c *FiveMinCalculator) Update(price float64, tradingVolume float64, currentPriceTime time.Time) {
	tick := tickData{
		Price:            price,
		TradingVolume:    tradingVolume,
		CurrentPriceTime: currentPriceTime,
	}

	currentWindowStart := currentPriceTime.Truncate(5 * time.Minute)

	if c.windowStart.IsZero() {
		// 初回
		c.windowStart = currentWindowStart
		c.windowStartVol = tradingVolume
		c.ticks = []tickData{tick}
		return
	}

	// 時間枠が変わった場合、古い枠のデータをフラッシュしてサマリを作成
	if currentWindowStart.After(c.windowStart) {
		c.flush(c.windowStart, currentWindowStart)

		// 新しい枠の開始
		c.windowStart = currentWindowStart
		if len(c.ticks) > 0 {
			lastTick := c.ticks[len(c.ticks)-1]
			c.windowStartVol = lastTick.TradingVolume
		} else {
			c.windowStartVol = tradingVolume
		}

		// 溜まっていたTickをクリアして今回のTickを入れる
		c.ticks = []tickData{tick}
	} else {
		// 同じ時間枠なら追加
		c.ticks = append(c.ticks, tick)
	}
}

// flush は指定期間のサマリを計算し、保存します
func (c *FiveMinCalculator) flush(windowStart time.Time, windowEnd time.Time) {
	if len(c.ticks) == 0 {
		return
	}

	lastTick := c.ticks[len(c.ticks)-1]
	startVol := c.windowStartVol
	periodVolume := lastTick.TradingVolume - startVol
	if periodVolume < 0 {
		periodVolume = 0
	}

	var sumPricedVolume float64
	var prevVolume = startVol

	for _, t := range c.ticks {
		tVol := t.TradingVolume - prevVolume
		if tVol > 0 {
			sumPricedVolume += t.Price * tVol
		}
		prevVolume = t.TradingVolume
	}

	var vwap float64
	if periodVolume > 0 {
		vwap = sumPricedVolume / periodVolume
	} else if len(c.ticks) > 0 {
		vwap = lastTick.Price
	}

	summary := FiveMinSummary{
		StartTime:     windowStart,
		EndTime:       windowEnd,
		TradingVolume: periodVolume,
		VWAP:          vwap,
	}

	c.fiveMinSummaries = append(c.fiveMinSummaries, summary)
}

// GetSummaries は現在までに確定している5分ごとのサマリ配列を返します
func (c *FiveMinCalculator) GetSummaries() []FiveMinSummary {
	// コピーして返す
	dst := make([]FiveMinSummary, len(c.fiveMinSummaries))
	copy(dst, c.fiveMinSummaries)
	return dst
}

// GetCurrentVWAP は現在蓄積中の5分枠のリアルタイムなVWAPを計算して返します
func (c *FiveMinCalculator) GetCurrentVWAP() float64 {
	if len(c.ticks) == 0 {
		return 0
	}

	lastTick := c.ticks[len(c.ticks)-1]
	startVol := c.windowStartVol
	periodVolume := lastTick.TradingVolume - startVol
	if periodVolume < 0 {
		periodVolume = 0
	}

	var sumPricedVolume float64
	var prevVolume = startVol

	for _, t := range c.ticks {
		tVol := t.TradingVolume - prevVolume
		if tVol > 0 {
			sumPricedVolume += t.Price * tVol
		}
		prevVolume = t.TradingVolume
	}

	if periodVolume > 0 {
		return sumPricedVolume / periodVolume
	}
	return lastTick.Price
}
