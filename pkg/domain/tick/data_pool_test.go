package tick_test

import (
	"fmt"
	"testing"

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
