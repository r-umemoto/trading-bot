package usecase_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/position"
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

type mockGateway struct {
	sendOrderFunc func(ctx context.Context, input order.SendOrderInput) (*order.Order, error)
}

func (m *mockGateway) Listen(ctx context.Context) (*market.MarketChannels, error) { return nil, nil }
func (m *mockGateway) DataPool() tick.DataPool { return nil }
func (m *mockGateway) SendOrder(ctx context.Context, input order.SendOrderInput) (*order.Order, error) {
	if m.sendOrderFunc != nil {
		return m.sendOrderFunc(ctx, input)
	}
	return input.Order, nil
}
func (m *mockGateway) CancelOrder(ctx context.Context, orderID string) error { return nil }
func (m *mockGateway) GetPositions(ctx context.Context, product order.ProductType) ([]position.Position, error) { return nil, nil }
func (m *mockGateway) GetOrders(ctx context.Context) (order.Orders, error) { return order.Orders{}, nil }
func (m *mockGateway) GetSymbol(ctx context.Context, symbolCode string, exchange order.ExchangeMarket) (symbol.Symbol, error) { return symbol.Symbol{}, nil }
func (m *mockGateway) RegisterSymbol(ctx context.Context, req market.ResisterSymbolRequest) error { return nil }
func (m *mockGateway) RegisterSymbols(ctx context.Context, reqs []market.ResisterSymbolRequest) error { return nil }
func (m *mockGateway) UnregisterSymbolAll(ctx context.Context) error { return nil }

type mockStrategy struct {
	target strategy.TargetPosition
}

func (m *mockStrategy) Name() string { return "mock" }
func (m *mockStrategy) Evaluate(input strategy.StrategyInput) strategy.TargetPosition { return m.target }
func (m *mockStrategy) AnalysisLogger() *slog.Logger { return nil }

func TestTradeUseCase_BypassAndSkipOrderDestruction(t *testing.T) {
	detail := symbol.Symbol{Code: "7203"}

	t.Run("ErrDispatchQueueBypass", func(t *testing.T) {
		var sendOrderCalled bool
		gw := &mockGateway{
			sendOrderFunc: func(ctx context.Context, input order.SendOrderInput) (*order.Order, error) {
				sendOrderCalled = true
				return nil, order.ErrDispatchQueueBypass
			},
		}

		strat := &mockStrategy{
			target: strategy.TargetPosition{
				Qty:       100,
				Price:     2500,
				OrderType: order.ORDER_TYPE_LIMIT,
				Reason:    "test_entry",
			},
		}

		s := sniper.NewSniper("test_sniper_7203", detail, strat, &strategy.NoopPolicy{}, order.EXCHANGE_TOSHO, nil)
		nest := sniper.NewSniperNest("7203", detail, []*sniper.Sniper{s}, nil)
		op := sniper.NewDefaultOperation("Op_7203", nest)

		tradeUC := usecase.NewTradeUseCase([]sniper.Operation{op}, gw, nil)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		tickCh := make(chan tick.Tick, 1)
		orderCh := make(chan order.Orders, 1)
		chs := &market.MarketChannels{
			Ticks:  map[string]<-chan tick.Tick{"7203": tickCh},
			Orders: map[string]<-chan order.Orders{"7203": orderCh},
		}

		tradeUC.Start(ctx, chs)

		tickCh <- tick.Tick{
			Symbol:           "7203",
			Price:            2500,
			CurrentPriceTime: time.Now(),
		}

		time.Sleep(50 * time.Millisecond)

		if !sendOrderCalled {
			t.Fatal("expected SendOrder to be called")
		}

		active := nest.GetActiveOrders()
		if len(active) != 0 {
			t.Errorf("expected 0 active orders after ErrDispatchQueueBypass, got %d", len(active))
		}
	})

	t.Run("ErrOrderSkipped", func(t *testing.T) {
		var sendOrderCalled bool
		gw := &mockGateway{
			sendOrderFunc: func(ctx context.Context, input order.SendOrderInput) (*order.Order, error) {
				sendOrderCalled = true
				return nil, order.ErrOrderSkipped
			},
		}

		strat := &mockStrategy{
			target: strategy.TargetPosition{
				Qty:       100,
				Price:     2500,
				OrderType: order.ORDER_TYPE_LIMIT,
				Reason:    "test_entry",
			},
		}

		s := sniper.NewSniper("test_sniper_7203", detail, strat, &strategy.NoopPolicy{}, order.EXCHANGE_TOSHO, nil)
		nest := sniper.NewSniperNest("7203", detail, []*sniper.Sniper{s}, nil)
		op := sniper.NewDefaultOperation("Op_7203", nest)

		tradeUC := usecase.NewTradeUseCase([]sniper.Operation{op}, gw, nil)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		tickCh := make(chan tick.Tick, 1)
		orderCh := make(chan order.Orders, 1)
		chs := &market.MarketChannels{
			Ticks:  map[string]<-chan tick.Tick{"7203": tickCh},
			Orders: map[string]<-chan order.Orders{"7203": orderCh},
		}

		tradeUC.Start(ctx, chs)

		tickCh <- tick.Tick{
			Symbol:           "7203",
			Price:            2500,
			CurrentPriceTime: time.Now(),
		}

		time.Sleep(50 * time.Millisecond)

		if !sendOrderCalled {
			t.Fatal("expected SendOrder to be called")
		}

		active := nest.GetActiveOrders()
		if len(active) != 0 {
			t.Errorf("expected 0 active orders after ErrOrderSkipped, got %d", len(active))
		}
	})
}
