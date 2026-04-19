package market

import (
	"testing"
	"time"
)

func TestOneMinBarIndicator(t *testing.T) {
	indicator := NewOneMinBarIndicator("1min_bar")

	baseTime := time.Date(2026, 4, 19, 9, 0, 0, 0, time.UTC)

	// Tick 1: 09:00:10 (Price 100, Cumulative Vol 1000) -> Bar 1 Start (Initial tick, volume diff ignored)
	indicator.Update(Tick{
		CurrentPriceTime: baseTime.Add(10 * time.Second),
		Price:            100,
		TradingVolume:    1000,
	})

	// Tick 2: 09:00:45 (Price 105, Cumulative Vol 1200) -> Bar 1 Update
	indicator.Update(Tick{
		CurrentPriceTime: baseTime.Add(45 * time.Second),
		Price:            105,
		TradingVolume:    1200,
	})

	// Tick 3: 09:00:55 (Price 95, Cumulative Vol 1500) -> Bar 1 Update
	indicator.Update(Tick{
		CurrentPriceTime: baseTime.Add(55 * time.Second),
		Price:            95,
		TradingVolume:    1500,
	})

	// Tick 4: 09:01:05 (Price 98, Cumulative Vol 1600) -> Bar 2 Start, Bar 1 Finished
	indicator.Update(Tick{
		CurrentPriceTime: baseTime.Add(1 * time.Minute).Add(5 * time.Second),
		Price:            98,
		TradingVolume:    1600,
	})

	val := indicator.Value()
	bars, ok := val.([]Bar)
	if !ok {
		t.Fatalf("expected []Bar, got %T", val)
	}

	if len(bars) != 2 {
		t.Fatalf("expected 2 bars, got %d", len(bars))
	}

	bar1 := bars[0]
	if bar1.Open != 100 {
		t.Errorf("expected open 100, got %f", bar1.Open)
	}
	if bar1.High != 105 {
		t.Errorf("expected high 105, got %f", bar1.High)
	}
	if bar1.Low != 95 {
		t.Errorf("expected low 95, got %f", bar1.Low)
	}
	if bar1.Close != 95 {
		t.Errorf("expected close 95, got %f", bar1.Close)
	}
	if bar1.Volume != 500 { // Tick 1 (1000) initialized, then 200 + 300 = 500
		t.Errorf("expected volume 500, got %f", bar1.Volume)
	}

	bar2 := bars[1]
	if bar2.Open != 98 {
		t.Errorf("expected open 98, got %f", bar2.Open)
	}
	if bar2.Volume != 100 { // 1600 - 1500 = 100
		t.Errorf("expected volume 100, got %f", bar2.Volume)
	}
}
