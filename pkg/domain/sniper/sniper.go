package sniper

import (
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/position"
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

// CalculateVirtualPosition は物理ポジションとアクティブ注文から約定予定分を含んだ仮想ポジションを計算します
func (s *Sniper) CalculateVirtualPosition(positions []position.Position, activeOrders []*order.Order) strategy.Position {
	var totalQty float64
	var totalCost float64
	for _, p := range positions {
		if p.Action == order.ACTION_SELL {
			totalQty -= p.LeavesQty
			totalCost -= p.Price * p.LeavesQty
		} else {
			totalQty += p.LeavesQty
			totalCost += p.Price * p.LeavesQty
		}
	}
	for _, curr := range activeOrders {
		if curr != nil && curr.IsFillExpected() {
			// 🌟 エントリー注文（新規建て）の約定予定のみを仮想ポジションに加算する。
			// 決済注文（返済）の約定予定は、物理ポジションから減算しない（決済完了までポジション維持として扱う）。
			if curr.CashMargin == order.CASH_MARGIN_MARGIN_ENTRY {
				switch curr.Action {
				case order.ACTION_BUY:
					totalQty += curr.OrderQty
					totalCost += curr.OrderPrice * curr.OrderQty
				case order.ACTION_SELL:
					totalQty -= curr.OrderQty
					totalCost -= curr.OrderPrice * curr.OrderQty
				}
			}
		}
	}
	avgPrice := 0.0
	if totalQty != 0 {
		avgPrice = math.Abs(totalCost / totalQty)
	}
	return strategy.Position{Qty: totalQty, AveragePrice: avgPrice}
}



