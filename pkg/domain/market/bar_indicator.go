package market

import (
	"time"
)

// OneMinBarIndicator は Tick データから1分足の Bar を生成・蓄積するインジケーターです。
type OneMinBarIndicator struct {
	id            string
	bars          []Bar
	currentBar    *Bar
	prevVolume    float64
	isInitialized bool
}

// NewOneMinBarIndicator は新しい OneMinBarIndicator を作成します。
func NewOneMinBarIndicator(id string) *OneMinBarIndicator {
	return &OneMinBarIndicator{
		id:   id,
		bars: make([]Bar, 0),
	}
}

// ID はこの指標の一意識別子を返します。
func (i *OneMinBarIndicator) ID() string {
	return i.id
}

// Update は Tick データを受け取り、1分足を集約します。
func (i *OneMinBarIndicator) Update(tick Tick) {
	if tick.Price <= 0 {
		return // 価格が無効な場合は処理しない
	}

	var tickVolume float64
	if !i.isInitialized {
		i.prevVolume = tick.TradingVolume
		i.isInitialized = true
		// 初回Tickの出来高は、それ以前の累積すべてを含んでいる可能性があるため差分ゼロとする
		tickVolume = 0
	} else {
		tickVolume = tick.TradingVolume - i.prevVolume
		if tickVolume < 0 {
			tickVolume = 0
		}
		i.prevVolume = tick.TradingVolume
	}

	// 現在のTickの時刻から「分」以下を切り捨てて1分のウィンドウ枠を決定する
	windowStart := tick.CurrentPriceTime.Truncate(time.Minute)

	if i.currentBar == nil {
		// 最初のバーを作成
		i.currentBar = &Bar{
			StartTime: windowStart,
			Open:      tick.Price,
			High:      tick.Price,
			Low:       tick.Price,
			Close:     tick.Price,
			Volume:    tickVolume,
		}
		return
	}

	if windowStart.After(i.currentBar.StartTime) {
		// 時間の枠が変わったので、現在のバーを確定させて保存し、新しいバーを開始する
		i.bars = append(i.bars, *i.currentBar)
		i.currentBar = &Bar{
			StartTime: windowStart,
			Open:      tick.Price,
			High:      tick.Price,
			Low:       tick.Price,
			Close:     tick.Price,
			Volume:    tickVolume,
		}
	} else {
		// 現在のバーのOHLCVを更新する
		if tick.Price > i.currentBar.High {
			i.currentBar.High = tick.Price
		}
		if tick.Price < i.currentBar.Low {
			i.currentBar.Low = tick.Price
		}
		i.currentBar.Close = tick.Price
		i.currentBar.Volume += tickVolume
	}
}

// Bars はこれまでに生成されたバーのリストを返します。
func (i *OneMinBarIndicator) Bars() []Bar {
	result := make([]Bar, 0, len(i.bars)+1)
	result = append(result, i.bars...)
	if i.currentBar != nil {
		result = append(result, *i.currentBar)
	}
	return result
}
