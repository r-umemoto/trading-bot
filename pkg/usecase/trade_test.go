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

type mockRejectError struct{}

func (mockRejectError) Error() string             { return "definitive reject" }
func (mockRejectError) IsRejected() bool          { return true }
func (mockRejectError) IsPositionMissing() bool   { return true }

type mockOtherRejectError struct{}

func (mockOtherRejectError) Error() string             { return "other reject" }
func (mockOtherRejectError) IsRejected() bool          { return true }
func (mockOtherRejectError) IsPositionMissing() bool   { return false }

func TestTradeUseCase_RejectedOrderCleanUpPosition(t *testing.T) {
	detail := symbol.Symbol{Code: "7203"}

	var sendOrderCalled bool
	gw := &mockGateway{
		sendOrderFunc: func(ctx context.Context, input order.SendOrderInput) (*order.Order, error) {
			sendOrderCalled = true
			return nil, mockRejectError{}
		},
	}

	strat := &mockStrategy{
		target: strategy.TargetPosition{
			Qty:    0,
			Price:  0,
			Reason: "test_exit",
		},
	}

	s := sniper.NewSniper("test_sniper_7203", detail, strat, &strategy.NoopPolicy{}, order.EXCHANGE_TOSHO, nil)
	nest := sniper.NewSniperNest("7203", detail, []*sniper.Sniper{s}, nil)
	op := sniper.NewDefaultOperation("Op_7203", nest)

	// 1. メモリ上に建玉をセット（注文を登録し、約定レポートを送ってポジションを生成）
	parentOrder := order.NewOrder("E_123", "7203", order.ACTION_BUY, 2500, 100, order.WithCashMargin(order.CASH_MARGIN_MARGIN_ENTRY))
	op.AddOrder("test_sniper_7203", parentOrder)

	// 約定された状態のレポートを送信
	filledOrder := *parentOrder
	filledOrder.CumQty = 100
	filledOrder.Executions = []order.Execution{
		{
			ID:            "E_123",
			Price:         2500,
			Qty:           100,
			ExecutionTime: time.Now(),
		},
	}
	filledOrder.BypassTransition(order.ORDER_STATUS_FILLED, order.STATE_CLOSED)

	op.UpdateOrders(order.Orders{
		Orders: []order.Order{filledOrder},
	})

	// 確認：建玉が1つあり、含み損益が計算できること
	if op.GetUnrealizedPnL("test_sniper_7203", 2600.0) != 10000.0 {
		t.Fatal("expected unrealized PnL to be 10000.0 initially")
	}

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

	// 拒絶されたので、建玉 "E_123" が PositionTracker から抹消されているはず
	pnl := op.GetUnrealizedPnL("test_sniper_7203", 2600.0)
	if pnl != 0.0 {
		t.Errorf("expected 0.0 PnL after order rejection (position deleted), got %f", pnl)
	}
}

func TestTradeUseCase_RejectedOrderKeepsPosition(t *testing.T) {
	detail := symbol.Symbol{Code: "7203"}

	var sendOrderCalled bool
	gw := &mockGateway{
		sendOrderFunc: func(ctx context.Context, input order.SendOrderInput) (*order.Order, error) {
			sendOrderCalled = true
			return nil, mockOtherRejectError{}
		},
	}

	strat := &mockStrategy{
		target: strategy.TargetPosition{
			Qty:    0,
			Price:  0,
			Reason: "test_exit",
		},
	}

	s := sniper.NewSniper("test_sniper_7203", detail, strat, &strategy.NoopPolicy{}, order.EXCHANGE_TOSHO, nil)
	nest := sniper.NewSniperNest("7203", detail, []*sniper.Sniper{s}, nil)
	op := sniper.NewDefaultOperation("Op_7203", nest)

	// 1. メモリ上に建玉をセット（注文を登録し、約定レポートを送ってポジションを生成）
	parentOrder := order.NewOrder("E_123", "7203", order.ACTION_BUY, 2500, 100, order.WithCashMargin(order.CASH_MARGIN_MARGIN_ENTRY))
	op.AddOrder("test_sniper_7203", parentOrder)

	// 約定された状態のレポートを送信
	filledOrder := *parentOrder
	filledOrder.CumQty = 100
	filledOrder.Executions = []order.Execution{
		{
			ID:            "E_123",
			Price:         2500,
			Qty:           100,
			ExecutionTime: time.Now(),
		},
	}
	filledOrder.BypassTransition(order.ORDER_STATUS_FILLED, order.STATE_CLOSED)

	op.UpdateOrders(order.Orders{
		Orders: []order.Order{filledOrder},
	})

	// 確認：建玉が1つあり、含み損益が計算できること
	if op.GetUnrealizedPnL("test_sniper_7203", 2600.0) != 10000.0 {
		t.Fatal("expected unrealized PnL to be 10000.0 initially")
	}

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

	// PositionMissingではないエラー（例: 余力不足など）による拒絶なので、建玉は維持されるべき
	pnl := op.GetUnrealizedPnL("test_sniper_7203", 2600.0)
	if pnl != 10000.0 {
		t.Errorf("expected 10000.0 PnL after non-missing-position order rejection (position kept), got %f", pnl)
	}
}

func TestTradeUseCase_OutofOrderReconciliation(t *testing.T) {
	detail := symbol.Symbol{Code: "7203"}

	strat := &mockStrategy{
		target: strategy.TargetPosition{Qty: 0},
	}

	s := sniper.NewSniper("test_sniper_7203", detail, strat, &strategy.NoopPolicy{}, order.EXCHANGE_TOSHO, nil)
	nest := sniper.NewSniperNest("7203", detail, []*sniper.Sniper{s}, nil)
	op := sniper.NewDefaultOperation("Op_7203", nest)

	// 最初はノーポジ（PnL = 0）
	if op.GetUnrealizedPnL("test_sniper_7203", 2600.0) != 0.0 {
		t.Fatal("expected 0 PnL initially")
	}

	// 1. 先に「決済（子）の約定」を適用する
	exitOrder := order.NewOrder(
		"exit_order_id",
		"7203",
		order.ACTION_SELL,
		2500,
		100,
		order.WithCashMargin(order.CASH_MARGIN_MARGIN_EXIT),
		order.WithRequest(&order.OrderRequest{
			ClosePositions: []order.ClosePosition{
				{HoldID: "E_999", Qty: 100}, // 未登録の親約定IDを指定
			},
		}),
	)
	op.AddOrder("test_sniper_7203", exitOrder) // 🌟 注文を追跡対象に登録！

	// 決済約定を追加
	exitOrder.Executions = []order.Execution{
		{
			ID:            "exit_exec_id",
			Price:         2600,
			Qty:           100,
			ExecutionTime: time.Now(),
		},
	}
	exitOrder.BypassTransition(order.ORDER_STATUS_FILLED, order.STATE_CLOSED)

	op.UpdateOrders(order.Orders{
		Orders: []order.Order{*exitOrder},
	})

	// 建玉がまだないので、消し込みは保留（スタック）され、PnL も 0 のままであること
	if op.GetUnrealizedPnL("test_sniper_7203", 2600.0) != 0.0 {
		t.Error("expected 0 PnL while exit execution is pending")
	}

	// 2. 後から「新規（親）の約定」を適用する
	parentOrder := order.NewOrder("E_999", "7203", order.ACTION_BUY, 2500, 100, order.WithCashMargin(order.CASH_MARGIN_MARGIN_ENTRY))
	op.AddOrder("test_sniper_7203", parentOrder) // 🌟 注文を追跡対象に登録！

	parentOrder.Executions = []order.Execution{
		{
			ID:            "E_999",
			Price:         2500,
			Qty:           100,
			ExecutionTime: time.Now(),
		},
	}
	parentOrder.BypassTransition(order.ORDER_STATUS_FILLED, order.STATE_CLOSED)

	op.UpdateOrders(order.Orders{
		Orders: []order.Order{*parentOrder},
	})

	// 親の約定によって建玉が生成された直後、保留されていた決済約定が自動解決され、
	// 結果として建玉が `LeavesQty == 0`（PnL = 0）になり、かつ確定損益として 10,000円が計上されていること
	if op.GetUnrealizedPnL("test_sniper_7203", 2600.0) != 0.0 {
		t.Errorf("expected 0.0 unrealized PnL (position fully closed), got %f", op.GetUnrealizedPnL("test_sniper_7203", 2600.0))
	}

	// 確定損益を確認
	obs := nest.PrepareObservation("test_sniper_7203", tick.Tick{Price: 2600}, &strategy.NoopPolicy{})
	if obs.Performance.RealizedPnL != 10000.0 {
		t.Errorf("expected 10000.0 realized PnL from the matched pending exit, got %f", obs.Performance.RealizedPnL)
	}
}

func TestTradeUseCase_OutofOrderReconciliation_MultiClose(t *testing.T) {
	detail := symbol.Symbol{Code: "7203"}

	strat := &mockStrategy{
		target: strategy.TargetPosition{Qty: 0},
	}

	s := sniper.NewSniper("test_sniper_7203", detail, strat, &strategy.NoopPolicy{}, order.EXCHANGE_TOSHO, nil)
	nest := sniper.NewSniperNest("7203", detail, []*sniper.Sniper{s}, nil)
	op := sniper.NewDefaultOperation("Op_7203", nest)

	// 1. 親建玉1 (E_1) だけを事前にメモリに登録しておく
	parentOrder1 := order.NewOrder("E_1", "7203", order.ACTION_BUY, 2500, 100, order.WithCashMargin(order.CASH_MARGIN_MARGIN_ENTRY))
	op.AddOrder("test_sniper_7203", parentOrder1)
	parentOrder1.Executions = []order.Execution{{ID: "E_1", Price: 2500, Qty: 100, ExecutionTime: time.Now()}}
	parentOrder1.BypassTransition(order.ORDER_STATUS_FILLED, order.STATE_CLOSED)
	op.UpdateOrders(order.Orders{Orders: []order.Order{*parentOrder1}})

	if op.GetUnrealizedPnL("test_sniper_7203", 2600.0) != 10000.0 {
		t.Fatal("expected 10000 unrealized PnL from E_1 initially")
	}

	// 2. 決済注文（E_1 と E_2 を指定）の約定を適用する (E_2 は未登録なので保留されるべき)
	exitOrder := order.NewOrder(
		"exit_order_id",
		"7203",
		order.ACTION_SELL,
		2500,
		200,
		order.WithCashMargin(order.CASH_MARGIN_MARGIN_EXIT),
		order.WithRequest(&order.OrderRequest{
			ClosePositions: []order.ClosePosition{
				{HoldID: "E_1", Qty: 100},
				{HoldID: "E_2", Qty: 100}, // 未登録
			},
		}),
	)
	op.AddOrder("test_sniper_7203", exitOrder)

	exitOrder.Executions = []order.Execution{
		{
			ID:            "exit_exec_id",
			Price:         2600,
			Qty:           200,
			ExecutionTime: time.Now(),
		},
	}
	exitOrder.BypassTransition(order.ORDER_STATUS_FILLED, order.STATE_CLOSED)

	op.UpdateOrders(order.Orders{Orders: []order.Order{*exitOrder}})

	// E_2がまだメモリにないため、適用は保留され、E_1 の建玉はまだ削除されずに残っているはず
	if op.GetUnrealizedPnL("test_sniper_7203", 2600.0) != 10000.0 {
		t.Error("expected E_1 to remain open since the exit execution should be pending due to missing E_2")
	}

	// 3. 後から親建玉2 (E_2) の約定を適用する
	parentOrder2 := order.NewOrder("E_2", "7203", order.ACTION_BUY, 2500, 100, order.WithCashMargin(order.CASH_MARGIN_MARGIN_ENTRY))
	op.AddOrder("test_sniper_7203", parentOrder2)
	parentOrder2.Executions = []order.Execution{{ID: "E_2", Price: 2500, Qty: 100, ExecutionTime: time.Now()}}
	parentOrder2.BypassTransition(order.ORDER_STATUS_FILLED, order.STATE_CLOSED)

	op.UpdateOrders(order.Orders{Orders: []order.Order{*parentOrder2}})

	// E_2が登録された瞬間に保留スタックが解決され、E_1 も E_2 も両方消し込まれるため、含み損益は 0.0 になること
	if op.GetUnrealizedPnL("test_sniper_7203", 2600.0) != 0.0 {
		t.Errorf("expected 0.0 unrealized PnL (both closed), got %f", op.GetUnrealizedPnL("test_sniper_7203", 2600.0))
	}

	// 両方の決済PnLが計上され、確定損益が 20,000円になっていること
	obs2 := nest.PrepareObservation("test_sniper_7203", tick.Tick{Price: 2600}, &strategy.NoopPolicy{})
	if obs2.Performance.RealizedPnL != 20000.0 {
		t.Errorf("expected 20000.0 realized PnL, got %f", obs2.Performance.RealizedPnL)
	}
}

func TestTradeUseCase_OutofOrderReconciliation_PartialClose(t *testing.T) {
	detail := symbol.Symbol{Code: "7203"}

	strat := &mockStrategy{
		target: strategy.TargetPosition{Qty: 0},
	}

	s := sniper.NewSniper("test_sniper_7203", detail, strat, &strategy.NoopPolicy{}, order.EXCHANGE_TOSHO, nil)
	nest := sniper.NewSniperNest("7203", detail, []*sniper.Sniper{s}, nil)
	op := sniper.NewDefaultOperation("Op_7203", nest)

	// 1. 決済注文（E_1 と E_2 を指定）の約定を適用する (両方未登録なので保留される)
	exitOrder := order.NewOrder(
		"exit_order_id",
		"7203",
		order.ACTION_SELL,
		2500,
		200,
		order.WithCashMargin(order.CASH_MARGIN_MARGIN_EXIT),
		order.WithRequest(&order.OrderRequest{
			ClosePositions: []order.ClosePosition{
				{HoldID: "E_1", Qty: 100},
				{HoldID: "E_2", Qty: 100},
			},
		}),
	)
	op.AddOrder("test_sniper_7203", exitOrder)

	exitOrder.Executions = []order.Execution{
		{
			ID:            "exit_exec_id",
			Price:         2600,
			Qty:           200,
			ExecutionTime: time.Now(),
		},
	}
	exitOrder.BypassTransition(order.ORDER_STATUS_FILLED, order.STATE_CLOSED)

	op.UpdateOrders(order.Orders{Orders: []order.Order{*exitOrder}})

	// 2. 先に親建玉1 (E_1) の約定だけが届く
	parentOrder1 := order.NewOrder("E_1", "7203", order.ACTION_BUY, 2500, 100, order.WithCashMargin(order.CASH_MARGIN_MARGIN_ENTRY))
	op.AddOrder("test_sniper_7203", parentOrder1)
	parentOrder1.Executions = []order.Execution{{ID: "E_1", Price: 2500, Qty: 100, ExecutionTime: time.Now()}}
	parentOrder1.BypassTransition(order.ORDER_STATUS_FILLED, order.STATE_CLOSED)

	op.UpdateOrders(order.Orders{Orders: []order.Order{*parentOrder1}})

	// E_1 は正常に消し込まれ（含み損益は 0.0 になるが、まだ E_2 が無いので全体の確定損益は 10,000円になっていること）
	obs1 := nest.PrepareObservation("test_sniper_7203", tick.Tick{Price: 2600}, &strategy.NoopPolicy{})
	if obs1.Performance.RealizedPnL != 10000.0 {
		t.Errorf("expected 10000.0 realized PnL after E_1 resolved, got %f", obs1.Performance.RealizedPnL)
	}

	// 3. 後から親建玉2 (E_2) の約定が届く
	parentOrder2 := order.NewOrder("E_2", "7203", order.ACTION_BUY, 2500, 100, order.WithCashMargin(order.CASH_MARGIN_MARGIN_ENTRY))
	op.AddOrder("test_sniper_7203", parentOrder2)
	parentOrder2.Executions = []order.Execution{{ID: "E_2", Price: 2500, Qty: 100, ExecutionTime: time.Now()}}
	parentOrder2.BypassTransition(order.ORDER_STATUS_FILLED, order.STATE_CLOSED)

	op.UpdateOrders(order.Orders{Orders: []order.Order{*parentOrder2}})

	// E_2も正常に消し込まれ、最終的に含み損益は 0.0、確定損益が 20,000円になっていること
	if op.GetUnrealizedPnL("test_sniper_7203", 2600.0) != 0.0 {
		t.Errorf("expected 0.0 unrealized PnL (both closed), got %f", op.GetUnrealizedPnL("test_sniper_7203", 2600.0))
	}

	obs2_multi := nest.PrepareObservation("test_sniper_7203", tick.Tick{Price: 2600}, &strategy.NoopPolicy{})
	if obs2_multi.Performance.RealizedPnL != 20000.0 {
		t.Errorf("expected 20000.0 final realized PnL, got %f", obs2_multi.Performance.RealizedPnL)
	}
}
