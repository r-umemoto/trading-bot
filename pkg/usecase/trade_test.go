package usecase_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/report"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
	"github.com/r-umemoto/trading-bot/pkg/infra/backtest"
	"github.com/r-umemoto/trading-bot/pkg/usecase"
)

type mockReportRepo struct {
	savedReport *report.DailyReport
	saveErr     error
}

func (m *mockReportRepo) Save(ctx context.Context, r *report.DailyReport) error {
	m.savedReport = r
	return m.saveErr
}

func TestTradeUseCase_Getters(t *testing.T) {
	gateway := backtest.NewSyncBacktestGateway(backtest.ExecutionModelTouch, 0)
	detail := symbol.Symbol{Code: "7203"}
	s := sniper.NewSniper("test_sniper_7203", detail, sniper.NewInstructionStrategy(), &strategy.NoopPolicy{}, order.EXCHANGE_TOSHO, nil)
	nest := sniper.NewSniperNest("7203", detail, []*sniper.Sniper{s}, nil)
	op := sniper.NewDefaultOperation("Op_7203", nest)

	tradeUC := usecase.NewTradeUseCase([]sniper.Operation{op}, gateway, nil)

	// Test GetPerformance
	perf := tradeUC.GetPerformance("test_sniper_7203")
	if perf.Trades != 0 {
		t.Errorf("expected Trades 0, got %d", perf.Trades)
	}

	// Test GetUnrealizedPnL
	pnl := tradeUC.GetUnrealizedPnL("test_sniper_7203", 2600.0)
	if pnl != 0 {
		t.Errorf("expected 0 pnl initially, got %f", pnl)
	}

	// Non-existent sniper
	perfNone := tradeUC.GetPerformance("non_existent")
	if perfNone.Trades != 0 {
		t.Error("expected empty performance for non-existent sniper")
	}
	pnlNone := tradeUC.GetUnrealizedPnL("non_existent", 2600.0)
	if pnlNone != 0 {
		t.Errorf("expected 0 pnl, got %f", pnlNone)
	}
}

func TestTradeUseCase_PrintPerformanceReport(t *testing.T) {
	gateway := backtest.NewSyncBacktestGateway(backtest.ExecutionModelTouch, 0)
	detail := symbol.Symbol{Code: "7203"}
	s := sniper.NewSniper("test_sniper_7203", detail, sniper.NewInstructionStrategy(), &strategy.NoopPolicy{}, order.EXCHANGE_TOSHO, nil)
	nest := sniper.NewSniperNest("7203", detail, []*sniper.Sniper{s}, nil)
	op := sniper.NewDefaultOperation("Op_7203", nest)

	repo := &mockReportRepo{}
	tradeUC := usecase.NewTradeUseCase([]sniper.Operation{op}, gateway, repo)

	tradeUC.PrintPerformanceReport(true)

	if repo.savedReport == nil {
		t.Fatal("expected report to be saved in repository")
	}
	if repo.savedReport.Total.Name != "Total" {
		t.Errorf("expected Total report name, got %s", repo.savedReport.Total.Name)
	}

	// Error saving path
	repoErr := &mockReportRepo{saveErr: errors.New("save error")}
	tradeUC2 := usecase.NewTradeUseCase([]sniper.Operation{op}, gateway, repoErr)
	tradeUC2.PrintPerformanceReport(true) // Should log error and not panic
}

func TestTradeUseCase_StartAndEventLoop(t *testing.T) {
	gateway := backtest.NewSyncBacktestGateway(backtest.ExecutionModelTouch, 0)
	detail := symbol.Symbol{Code: "7203"}
	s := sniper.NewSniper("test_sniper_7203", detail, sniper.NewInstructionStrategy(), &strategy.NoopPolicy{}, order.EXCHANGE_TOSHO, nil)
	nest := sniper.NewSniperNest("7203", detail, []*sniper.Sniper{s}, nil)
	op := sniper.NewDefaultOperation("Op_7203", nest)

	tradeUC := usecase.NewTradeUseCase([]sniper.Operation{op}, gateway, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Prepare channels
	tickCh := make(chan tick.Tick, 10)
	orderCh := make(chan order.Orders, 10)
	chs := &market.MarketChannels{
		Ticks:  map[string]<-chan tick.Tick{"7203": tickCh},
		Orders: map[string]<-chan order.Orders{"7203": orderCh},
	}

	// Start TradeUseCase event loop
	tradeUC.Start(ctx, chs)

	// Send a tick
	testTick := tick.Tick{
		Symbol:           "7203",
		Price:            2550.0,
		CurrentPriceTime: time.Now(),
	}
	tickCh <- testTick

	// Wait a moment for it to be handled
	time.Sleep(10 * time.Millisecond)

	// Send an order update
	ords := order.Orders{
		Orders: []order.Order{
			*order.NewOrder("order-id-123", "7203", order.ACTION_BUY, 2550.0, 100),
		},
	}
	orderCh <- ords

	time.Sleep(10 * time.Millisecond)
}
