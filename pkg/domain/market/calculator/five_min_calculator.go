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

	// 時間枠が変わった場合、古い枠のデータを確定してサマリを作成
	if currentWindowStart.After(c.windowStart) {
		c.Save(c.windowStart, currentWindowStart)

		// 新しい時間枠の開始地点の出来高を特定（前の枠の最後の出来高）
		if len(c.ticks) > 0 {
			c.windowStartVol = c.ticks[len(c.ticks)-1].TradingVolume
		} else {
			c.windowStartVol = tradingVolume
		}
		c.windowStart = currentWindowStart
	}

	c.ticks = append(c.ticks, tick)

	// バッファの掃除
	// 「現在の固定枠の開始」と「5分スライディングの開始」のいずれか古い方より前のデータを消去
	slidingCutoff := currentPriceTime.Add(-5 * time.Minute)
	fixedCutoff := c.windowStart
	
	cutoff := slidingCutoff
	if fixedCutoff.Before(cutoff) {
		cutoff = fixedCutoff
	}

	for len(c.ticks) > 0 && c.ticks[0].CurrentPriceTime.Before(cutoff) {
		c.ticks = c.ticks[1:]
	}
}

// Save は指定期間のサマリを計算し、保存します
func (c *FiveMinCalculator) Save(windowStart time.Time, windowEnd time.Time) {
	// 指定されたウィンドウ（[windowStart, windowEnd)）に属するTickのみを抽出
	var windowTicks []tickData
	for _, t := range c.ticks {
		if !t.CurrentPriceTime.Before(windowStart) && t.CurrentPriceTime.Before(windowEnd) {
			windowTicks = append(windowTicks, t)
		}
	}

	if len(windowTicks) == 0 {
		return
	}

	lastTick := windowTicks[len(windowTicks)-1]
	startVol := c.windowStartVol
	periodVolume := lastTick.TradingVolume - startVol
	if periodVolume < 0 {
		periodVolume = 0
	}

	var sumPricedVolume float64
	var prevVolume = startVol

	for _, t := range windowTicks {
		tVol := t.TradingVolume - prevVolume
		if tVol > 0 {
			sumPricedVolume += t.Price * tVol
		}
		prevVolume = t.TradingVolume
	}

	var vwap float64
	if periodVolume > 0 {
		vwap = sumPricedVolume / periodVolume
	} else {
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

// GetCurrentVWAP は直近5分間のスライディングウィンドウに基づいたリアルタイムなVWAPを計算して返します
func (c *FiveMinCalculator) GetCurrentVWAP() float64 {
	if len(c.ticks) == 0 {
		return 0
	}

	lastTick := c.ticks[len(c.ticks)-1]
	// スライディングウィンドウの開始時刻（現在から5分前）
	slidingStart := lastTick.CurrentPriceTime.Add(-5 * time.Minute)

	var startVol float64
	var sumPricedVolume float64
	var prevVolume float64
	first := true

	for _, t := range c.ticks {
		// ウィンドウ開始前のデータは、開始時点の出来高（ベースライン）を特定するために使う
		if t.CurrentPriceTime.Before(slidingStart) {
			startVol = t.TradingVolume
			continue
		}

		if first {
			// ウィンドウ開始以降の最初のデータ
			if startVol == 0 {
				startVol = t.TradingVolume
			}
			prevVolume = startVol
			first = false
		}

		tVol := t.TradingVolume - prevVolume
		if tVol > 0 {
			sumPricedVolume += t.Price * tVol
		}
		prevVolume = t.TradingVolume
	}

	periodVolume := lastTick.TradingVolume - startVol
	if periodVolume > 0 {
		return sumPricedVolume / periodVolume
	}
	return lastTick.Price
}
