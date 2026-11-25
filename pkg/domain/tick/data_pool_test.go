package tick_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

// DummyFetcherIndicator はテスト用の FetcherIndicator 実装です
type DummyFetcherIndicator struct {
	id    string
	value float64
}

var _ tick.Indicator = (*DummyFetcherIndicator)(nil)
var _ tick.FetcherIndicator = (*DummyFetcherIndicator)(nil)

func (d *DummyFetcherIndicator) ID() string {
	return d.id
}

func (d *DummyFetcherIndicator) Update(t tick.Tick) {}

func (d *DummyFetcherIndicator) Dependencies() []tick.Indicator {
	return nil
}

func (d *DummyFetcherIndicator) FetchAndInitialize(feeder tick.HistoricalFeeder) error {
	val, err := feeder.FetchSMA(75)
	if err != nil {
		return err
	}
	d.value = val
	return nil
}

// DummyFeeder はテスト用の HistoricalFeeder 実装です
type DummyFeeder struct{}

func (d *DummyFeeder) FetchSMA(period int) (float64, error) {
	if period == 75 {
		return 3500.5, nil
	}
	return 0, fmt.Errorf("unexpected period: %d", period)
}

func (d *DummyFeeder) FetchPreviousClose() (float64, error) {
	return 3500.5, nil
}

// DummyFeederProvider はテスト用の HistoricalFeederProvider 実装です
type DummyFeederProvider struct{}

func (d *DummyFeederProvider) GetFeeder(symbol string) tick.HistoricalFeeder {
	return &DummyFeeder{}
}

func TestDataPool_FetcherIndicatorInitialization(t *testing.T) {
	// 1. データプールをモックプロバイダー付きで生成
	provider := &DummyFeederProvider{}
	pool := tick.NewDefaultDataPool(provider)

	// 2. 指標を生成して登録
	symbol := "6758"
	indicatorID := "daily_sma_75"

	ind := pool.GetOrCreateIndicator(symbol, indicatorID, func() tick.Indicator {
		return &DummyFetcherIndicator{id: indicatorID}
	})

	// 3. 型キャストして正しく初期値がロードされているかを検証
	fetcher, ok := ind.(*DummyFetcherIndicator)
	if !ok {
		t.Fatalf("expected DummyFetcherIndicator, got %T", ind)
	}

	expectedVal := 3500.5
	if fetcher.value != expectedVal {
		t.Errorf("expected initialized value to be %f, got %f", expectedVal, fetcher.value)
	}
}

func TestDataPool_OrderRTTSamples_RingBufferOrder(t *testing.T) {
	pool := tick.NewDefaultDataPool(nil)

	// Before wrap-around: push 5 samples (10..50ms)
	for i := 1; i <= 5; i++ {
		pool.RecordOrderRTT(time.Duration(i*10) * time.Millisecond)
	}

	buf := make([]float64, 3)
	n := pool.OrderRTTSamples(buf)
	if n != 3 {
		t.Fatalf("expected 3 samples, got %d", n)
	}
	wantBeforeWrap := []float64{30, 40, 50}
	for i, want := range wantBeforeWrap {
		if buf[i] != want {
			t.Errorf("before wrap buf[%d] = %v, want %v (full=%v)", i, buf[i], want, buf)
		}
	}

	// After wrap-around: push 35 more samples (total 40: 10..400ms, window size is 30 -> 110..400)
	for i := 6; i <= 40; i++ {
		pool.RecordOrderRTT(time.Duration(i*10) * time.Millisecond)
	}

	// Full window (30 samples) should be in chronological order: 110, 120, ..., 400
	full := make([]float64, 30)
	n = pool.OrderRTTSamples(full)
	if n != 30 {
		t.Fatalf("expected 30 samples, got %d", n)
	}
	for i := 0; i < 30; i++ {
		want := float64((11 + i) * 10)
		if full[i] != want {
			t.Errorf("after wrap full[%d] = %v, want %v", i, full[i], want)
		}
	}

	// Smaller dst (5 samples) should return the 5 most recent samples in chronological order: 360..400
	recent := make([]float64, 5)
	n = pool.OrderRTTSamples(recent)
	if n != 5 {
		t.Fatalf("expected 5 samples, got %d", n)
	}
	wantRecent := []float64{360, 370, 380, 390, 400}
	for i, want := range wantRecent {
		if recent[i] != want {
			t.Errorf("after wrap recent[%d] = %v, want %v (recent=%v)", i, recent[i], want, recent)
		}
	}
}
