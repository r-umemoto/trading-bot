package usecase_test

import (
	"context"
	"errors"
	"testing"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/infra/backtest"
	"github.com/r-umemoto/trading-bot/pkg/usecase"
)

type errorGateway struct {
	*backtest.SyncBacktestGateway
	registerErr   error
	unregisterErr error
}

func (g *errorGateway) RegisterSymbols(ctx context.Context, reqs []market.ResisterSymbolRequest) error {
	if g.registerErr != nil {
		return g.registerErr
	}
	return g.SyncBacktestGateway.RegisterSymbols(ctx, reqs)
}

func (g *errorGateway) UnregisterSymbolAll(ctx context.Context) error {
	if g.unregisterErr != nil {
		return g.unregisterErr
	}
	return g.SyncBacktestGateway.UnregisterSymbolAll(ctx)
}

func TestSystemUseCase_Initialize_RegisterError(t *testing.T) {
	bg := backtest.NewSyncBacktestGateway(backtest.ExecutionModelTouch, 0)
	mockErr := errors.New("register error")
	eg := &errorGateway{
		SyncBacktestGateway: bg,
		registerErr:         mockErr,
	}

	detail := symbol.Symbol{Code: "7203"}
	s := sniper.NewSniper("test_sniper", detail, sniper.NewInstructionStrategy(), &strategy.NoopPolicy{}, order.EXCHANGE_TOSHO, nil)
	nest := sniper.NewSniperNest("7203", detail, []*sniper.Sniper{s}, nil)
	op := sniper.NewDefaultOperation("Op_7203", nest)

	su := usecase.NewSystemUseCase([]sniper.Operation{op}, eg)

	err := su.Initialize(context.Background())
	if !errors.Is(err, mockErr) {
		t.Errorf("expected error %v, got %v", mockErr, err)
	}
}

func TestSystemUseCase_Shutdown_UnregisterError(t *testing.T) {
	bg := backtest.NewSyncBacktestGateway(backtest.ExecutionModelTouch, 0)
	mockErr := errors.New("unregister error")
	eg := &errorGateway{
		SyncBacktestGateway: bg,
		unregisterErr:       mockErr,
	}

	detail := symbol.Symbol{Code: "7203"}
	s := sniper.NewSniper("test_sniper", detail, sniper.NewInstructionStrategy(), &strategy.NoopPolicy{}, order.EXCHANGE_TOSHO, nil)
	nest := sniper.NewSniperNest("7203", detail, []*sniper.Sniper{s}, nil)
	op := sniper.NewDefaultOperation("Op_7203", nest)

	su := usecase.NewSystemUseCase([]sniper.Operation{op}, eg)

	err := su.Shutdown(context.Background())
	if !errors.Is(err, mockErr) {
		t.Errorf("expected error %v, got %v", mockErr, err)
	}
}

func TestSystemUseCase_Initialize_DuplicateFilter(t *testing.T) {
	bg := backtest.NewSyncBacktestGateway(backtest.ExecutionModelTouch, 0)

	// Create two operations with duplicate symbol codes and exchanges
	detail := symbol.Symbol{Code: "7203"}
	s1 := sniper.NewSniper("test_sniper_1", detail, sniper.NewInstructionStrategy(), &strategy.NoopPolicy{}, order.EXCHANGE_TOSHO, nil)
	nest1 := sniper.NewSniperNest("7203", detail, []*sniper.Sniper{s1}, nil)
	op1 := sniper.NewDefaultOperation("Op_7203_1", nest1)

	s2 := sniper.NewSniper("test_sniper_2", detail, sniper.NewInstructionStrategy(), &strategy.NoopPolicy{}, order.EXCHANGE_TOSHO, nil)
	nest2 := sniper.NewSniperNest("7203", detail, []*sniper.Sniper{s2}, nil)
	op2 := sniper.NewDefaultOperation("Op_7203_2", nest2)

	su := usecase.NewSystemUseCase([]sniper.Operation{op1, op2}, bg)

	// Initialize should complete successfully and the duplicate should have been filtered
	err := su.Initialize(context.Background())
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}
}
