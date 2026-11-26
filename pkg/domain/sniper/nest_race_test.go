package sniper_test

import (
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

type raceTestStrategy struct {
	name   string
	target strategy.TargetPosition
}

func (s *raceTestStrategy) Name() string                  { return s.name }
func (s *raceTestStrategy) AnalysisLogger() *slog.Logger  { return nil }
func (s *raceTestStrategy) Evaluate(in strategy.StrategyInput) strategy.TargetPosition {
	return s.target
}

func TestSniperNest_ConcurrentHandleTickAndOrderMutations(t *testing.T) {
	modes := []sniper.ReconciliationMode{
		sniper.ReconciliationModeLegacy,
		sniper.ReconciliationModeAggregate,
	}

	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			detail := symbol.Symbol{Code: "7203", PriceRangeGroup: symbol.PRICE_RANGE_GROUP_TSE_STANDARD}
			longStrat := &raceTestStrategy{
				name: "long",
				target: strategy.TargetPosition{
					Qty:       100,
					Price:     2500,
					OrderType: order.ORDER_TYPE_LIMIT,
					Reason:    "race_long",
				},
			}
			shortStrat := &raceTestStrategy{
				name: "short",
				target: strategy.TargetPosition{
					Qty:       -100,
					Price:     2510,
					OrderType: order.ORDER_TYPE_LIMIT,
					Reason:    "race_short",
				},
			}

			s1 := sniper.NewSniper("race_long_7203", detail, longStrat, &strategy.TouchTTLPolicy{TTL: 100 * time.Millisecond}, order.EXCHANGE_TOSHO, nil)
			s2 := sniper.NewSniper("race_short_7203", detail, shortStrat, &strategy.TouchTTLPolicy{TTL: 100 * time.Millisecond}, order.EXCHANGE_TOSHO, nil)
			nest := sniper.NewSniperNest("7203", detail, []*sniper.Sniper{s1, s2}, nil)
			nest.SetReconciliationMode(mode)

			var wg sync.WaitGroup
			done := make(chan struct{})

			// Goroutine 1: Simulate async SendOrder mutating returned OrderBullet.Order + callbacks
			actionCh := make(chan sniper.FireAction, 256)
			wg.Add(1)
			go func() {
				defer wg.Done()
				seq := 0
				for act := range actionCh {
					ordBullet, ok := act.Bullet.(sniper.OrderBullet)
					if !ok || ordBullet.Order == nil {
						continue
					}
					seq++
					// インフラ層(SendOrderRaw / Dispatcher)による構造体書き換えを模倣
					ordBullet.Order.ID = fmt.Sprintf("server-%d", seq)
					ordBullet.Order.ToWaiting()
					ordBullet.Order.ToPending()
					ordBullet.Order.ToActive()

					if seq%3 == 0 {
						nest.FailSendingOrder(act.SniperID, ordBullet.Order)
					} else if seq%5 == 0 {
						nest.DestroySendingOrder(act.SniperID, ordBullet.Order)
					} else {
						nest.UpdateOrderID(act.SniperID, ordBullet.Order, ordBullet.Order.ID)
					}
				}
			}()

			// Goroutine 2: Concurrent polling UpdateOrders + read accessors
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-done:
						return
					default:
						active := nest.GetActiveOrders()
						for _, o := range active {
							_ = o.ID
							_ = o.Status()
						}
						_ = nest.GetCommittedPosition("race_long_7203")
						_ = nest.GetTotalCommittedPosition()
						nest.UpdateOrders(order.Orders{})
					}
				}
			}()

			// Main loop: HandleTick
			base := time.Now()
			for i := 0; i < 150; i++ {
				price := 2500.0 + float64(i%15)
				actions := nest.HandleTick(tick.Tick{
					Symbol:           "7203",
					Price:            price,
					CurrentPriceTime: base.Add(time.Duration(i) * 50 * time.Millisecond),
				})
				for _, act := range actions {
					actionCh <- act
				}
			}

			close(actionCh)
			close(done)
			wg.Wait()
		})
	}
}
