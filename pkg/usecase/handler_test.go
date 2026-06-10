package usecase_test

import (
	"context"
	"testing"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/infra/backtest"
	"github.com/r-umemoto/trading-bot/pkg/usecase"
)

func TestUseCaseHandler_Lifecycle(t *testing.T) {
	// 1. Setup mock gateway
	gateway := backtest.NewSyncBacktestGateway(backtest.ExecutionModelTouch, 0)

	// 2. Setup sniper and operation
	detail := symbol.Symbol{Code: "7203"}
	strategyA := sniper.NewInstructionStrategy()
	policy := &strategy.NoopPolicy{}
	s := sniper.NewSniper("test_sniper_7203", detail, strategyA, policy, order.EXCHANGE_TOSHO, nil)
	nest := sniper.NewSniperNest("7203", detail, []*sniper.Sniper{s}, nil)
	op := sniper.NewDefaultOperation("Op_7203", nest)

	operations := []sniper.Operation{op}

	// 3. Create usecases
	systemUC := usecase.NewSystemUseCase(operations, gateway)
	tradeUC := usecase.NewTradeUseCase(operations, gateway, nil)

	// 4. Create handler
	handler := usecase.NewUseCaseHandler(systemUC, tradeUC)
	if handler == nil {
		t.Fatal("expected NewUseCaseHandler to return a non-nil handler")
	}

	ctx := context.Background()

	// 5. Test Start
	err := handler.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// 6. Test PrintReport
	handler.PrintReport(false)
	handler.PrintReport(true)

	// 7. Test Shutdown
	err = handler.Shutdown(ctx)
	if err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}
}
