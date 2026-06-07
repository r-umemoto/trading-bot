package sniper

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
)

type Strategy interface {
	Name() string
	Evaluate(input strategy.StrategyInput) strategy.TargetPosition
	AnalysisLogger() *slog.Logger
}

type PerformanceProvider interface {
	GetPerformance(sniperID string) Performance
	GetUnrealizedPnL(sniperID string, currentPrice float64) float64
}

type LifecycleState int

const (
	LifecycleActive LifecycleState = iota
	LifecycleExiting
	LifecycleStopped
)

type Bullet interface {
	isBullet()
}

type OrderBullet struct {
	Order *order.Order
}

func (OrderBullet) isBullet() {}

type CancelBullet struct {
	OrderID string
}

func (CancelBullet) isBullet() {}

type Performance struct {
	Trades        int
	Wins          int
	Losses        int
	RealizedPnL   float64
	UnrealizedPnL float64
}

type Sniper struct {
	ID                string
	Detail            symbol.Symbol
	Strategy          Strategy
	State             strategy.StrategyState
	ExecutionPolicy   strategy.ExecutionPolicy
	Logger            *slog.Logger
	mu                sync.Mutex
	lifecycle         LifecycleState
	AccountType       order.AccountType
	Exchange          order.ExchangeMarket
	MarginTradeType   order.MarginTradeType

	lastSignalReason string
	lastStatusLogAt  time.Time
}

func NewSniper(id string, detail symbol.Symbol, strategy Strategy, policy strategy.ExecutionPolicy, exchange order.ExchangeMarket, logger *slog.Logger) *Sniper {
	if logger == nil {
		logger = strategy.AnalysisLogger()
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Sniper{
		ID:                  id,
		Detail:              detail,
		Strategy:            strategy,
		ExecutionPolicy:     policy,
		AccountType:         order.ACCOUNT_SPECIAL,
		Exchange:            exchange,
		MarginTradeType:     order.TRADE_TYPE_GENERAL_DAY,
		Logger:              logger,
		lifecycle:           LifecycleActive,
	}
}

func (s *Sniper) Evaluate(input strategy.StrategyInput) strategy.TargetPosition {
	s.mu.Lock()
	defer s.mu.Unlock()

	target := s.Strategy.Evaluate(input)

	if target.Price > 0 {
		target.Price = s.Detail.RoundPrice(target.Price)
	}
	if target.HasIfDone && target.ExitPrice > 0 {
		target.ExitPrice = s.Detail.RoundPrice(target.ExitPrice)
	}

	// ライフサイクル管理
	if s.lifecycle == LifecycleExiting {
		target = strategy.TargetPosition{
			Qty:       0,
			Price:     0,
			OrderType: order.ORDER_TYPE_MARKET,
			Reason:    "LIFECYCLE_FORCE_EXIT",
		}
	}

	if target.Reason != "" {
		s.lastSignalReason = target.Reason
	}

	s.logStatus(input)

	return target
}

func (s *Sniper) logStatus(input strategy.StrategyInput) {
	if time.Since(s.lastStatusLogAt) < 1*time.Second {
		return
	}
	s.Logger.Info("STRATEGY_STATUS",
		slog.String("symbol", s.Detail.Code),
		slog.Float64("price", input.LatestTick.Price),
		slog.Float64("hold_qty", input.HoldQty()),
	)
	s.lastStatusLogAt = time.Now()
}

func (s *Sniper) OrderlyExit() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lifecycle = LifecycleExiting
	s.Logger.Warn("LIFECYCLE_EXIT_TRIGGERED", slog.String("symbol", s.Detail.Code))
}

func (s *Sniper) ForceStop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lifecycle = LifecycleStopped
	s.Logger.Error("LIFECYCLE_STOP_TRIGGERED", slog.String("symbol", s.Detail.Code))
}

func (s *Sniper) GetLifecycle() LifecycleState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lifecycle
}

func (s *Sniper) ForceExit() {
	s.ForceStop()
	fmt.Printf("🚨 [%s] 強制停止モードON。\n", s.Detail.Code)
}

func (s *Sniper) GetSymbolCode() string {
	return s.Detail.Code
}

func (s *Sniper) GetID() string {
	return s.ID
}

func (s *Sniper) GetStrategyName() string {
	return s.Strategy.Name()
}



