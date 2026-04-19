package market

import "time"

// Bar は特定期間の価格データ（ローソク足）のOHLCVを表します。
type Bar struct {
	StartTime time.Time
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    float64
}
