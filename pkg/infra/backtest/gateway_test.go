package backtest

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

func TestGatewayLatency_OrderMatchingDelay(t *testing.T) {
	// Latency 100ms
	g := NewBacktestGateway(ExecutionModelTouch, 100*time.Millisecond)

	baseTime, _ := time.Parse("15:04:05.000", "10:00:00.000")

	// 最初のTickで時間を進める
	g.ProcessTick(tick.Tick{
		Symbol:            "6758",
		Price:             1000.0,
		CurrentPriceTime: baseTime,
	})

	// 10:00:00.000 に指値買い注文を送信
	ord, err := g.SendOrder(context.Background(), order.SendOrderInput{
		Order: &order.Order{
			Symbol:     "6758",
			Action:     order.ACTION_BUY,
			OrderQty:   100,
			OrderPrice: 990,
			Type:       order.ORDER_TYPE_LIMIT,
		},
	})
	if err != nil {
		t.Fatalf("SendOrder failed: %v", err)
	}

	if ord.ID == "" {
		t.Errorf("expected generated order ID, got empty string")
	}

	// 50ms経過: 10:00:00.050 (価格990にタッチするが、まだ遅延中で未到達のはず)
	tickTime50, _ := time.Parse("15:04:05.000", "10:00:00.050")
	g.ProcessTick(tick.Tick{
		Symbol:            "6758",
		Price:             990.0,
		CurrentPriceTime: tickTime50,
	})

	orders, _ := g.GetOrders(context.Background())
	if orders.Orders[0].Status() == order.ORDER_STATUS_FILLED {
		t.Errorf("expected order not to be filled at 50ms (before latency delay)")
	}

	// 100ms経過: 10:00:00.100 (価格990にタッチ。取引所に到達しているため約定するはず)
	tickTime100, _ := time.Parse("15:04:05.000", "10:00:00.100")
	g.ProcessTick(tick.Tick{
		Symbol:            "6758",
		Price:             990.0,
		CurrentPriceTime: tickTime100,
	})

	orders, _ = g.GetOrders(context.Background())
	if orders.Orders[0].Status() != order.ORDER_STATUS_FILLED {
		t.Errorf("expected order to be filled at 100ms, but got: %v", orders.Orders[0].Status())
	}
}

func TestGatewayLatency_CancelRace(t *testing.T) {
	// Latency 100ms
	g := NewBacktestGateway(ExecutionModelTouch, 100*time.Millisecond)

	baseTime, _ := time.Parse("15:04:05.000", "10:00:00.000")

	// 10:00:00.000 に到達
	g.ProcessTick(tick.Tick{
		Symbol:            "6758",
		Price:             1000.0,
		CurrentPriceTime: baseTime,
	})

	// 指値注文送信 (到達予定: 10:00:00.100)
	ord, _ := g.SendOrder(context.Background(), order.SendOrderInput{
		Order: &order.Order{
			Symbol:     "6758",
			Action:     order.ACTION_BUY,
			OrderQty:   100,
			OrderPrice: 990,
			Type:       order.ORDER_TYPE_LIMIT,
		},
	})

	// 100ms経過: 10:00:00.100 (取引所に到達)
	tickTime100, _ := time.Parse("15:04:05.000", "10:00:00.100")
	g.ProcessTick(tick.Tick{
		Symbol:            "6758",
		Price:             1000.0,
		CurrentPriceTime: tickTime100,
	})

	// 10:00:00.110 にキャンセル要求 (キャンセル到達予定: 10:00:00.210)
	tickTime110, _ := time.Parse("15:04:05.000", "10:00:00.110")
	g.ProcessTick(tick.Tick{
		Symbol:            "6758",
		Price:             1000.0,
		CurrentPriceTime: tickTime110,
	})

	err := g.CancelOrder(context.Background(), ord.ID)
	if err != nil {
		t.Fatalf("CancelOrder failed: %v", err)
	}

	orders, _ := g.GetOrders(context.Background())
	if orders.Orders[0].Status() != order.ORDER_STATUS_CANCEL_SENT {
		t.Errorf("expected status ORDER_STATUS_CANCEL_SENT, got %v", orders.Orders[0].Status())
	}

	// 10:00:00.150 (キャンセル到達前の価格タッチ。約定が優先されるはず)
	tickTime150, _ := time.Parse("15:04:05.000", "10:00:00.150")
	g.ProcessTick(tick.Tick{
		Symbol:            "6758",
		Price:             990.0,
		CurrentPriceTime: tickTime150,
	})

	orders, _ = g.GetOrders(context.Background())
	if orders.Orders[0].Status() != order.ORDER_STATUS_FILLED {
		t.Errorf("expected order to be filled before cancel reaches exchange, but got %v", orders.Orders[0].Status())
	}

	// 10:00:00.220 (キャンセル到達予定時刻を過ぎたTick)
	tickTime220, _ := time.Parse("15:04:05.000", "10:00:00.220")
	g.ProcessTick(tick.Tick{
		Symbol:            "6758",
		Price:             990.0,
		CurrentPriceTime: tickTime220,
	})

	// 既に約定しているのでステータスはFILLEDのままであること
	orders, _ = g.GetOrders(context.Background())
	if orders.Orders[0].Status() != order.ORDER_STATUS_FILLED {
		t.Errorf("expected order to remain FILLED, but got %v", orders.Orders[0].Status())
	}
}

func TestGatewayLatency_VolumeModelDeferredDepth(t *testing.T) {
	// Latency 100ms, Volumeモデル
	g := NewBacktestGateway(ExecutionModelVolume, 100*time.Millisecond)

	baseTime, _ := time.Parse("15:04:05.000", "10:00:00.000")

	// 10:00:00.000 (発注送信時のTick板: 買い板の価格990に500株あるとする)
	g.ProcessTick(tick.Tick{
		Symbol:        "6758",
		Price:         1000.0,
		TradingVolume: 10000,
		BuyBoard: []tick.Quote{
			{Price: 990, Qty: 500},
		},
		CurrentPriceTime: baseTime,
	})

	// 10:00:00.000 に指値買い注文 (990円) を送信 (到達予定: 10:00:00.100)
	ord, _ := g.SendOrder(context.Background(), order.SendOrderInput{
		Order: &order.Order{
			Symbol:     "6758",
			Action:     order.ACTION_BUY,
			OrderQty:   100,
			OrderPrice: 990,
			Type:       order.ORDER_TYPE_LIMIT,
		},
	})

	// 10:00:00.050 (注文はまだ届いていないが、板の990円が1000株に増える)
	tickTime50, _ := time.Parse("15:04:05.000", "10:00:00.050")
	g.ProcessTick(tick.Tick{
		Symbol:        "6758",
		Price:         1000.0,
		TradingVolume: 10100,
		BuyBoard: []tick.Quote{
			{Price: 990, Qty: 1000},
		},
		CurrentPriceTime: tickTime50,
	})

	// 10:00:00.100 (注文が到達するTick。この時の板の990円は300株。初期キュー深度はこの300株になるべき)
	tickTime100, _ := time.Parse("15:04:05.000", "10:00:00.100")
	g.ProcessTick(tick.Tick{
		Symbol:        "6758",
		Price:         990.0, // 現値が同値にタッチ
		TradingVolume: 10200,
		BuyBoard: []tick.Quote{
			{Price: 990, Qty: 300},
		},
		CurrentPriceTime: tickTime100,
	})

	// 到達時の初期深度が300株になっていることを確認
	if depth, ok := g.initialDepths[ord.ID]; !ok || depth != 300 {
		t.Errorf("expected initial depth to be deferred initialized to 300, got %v", depth)
	}

	// 10:00:00.150 (同値990円での累積出来高がさらに400増える。400 > 300 なので約定するはず)
	tickTime150, _ := time.Parse("15:04:05.000", "10:00:00.150")
	g.ProcessTick(tick.Tick{
		Symbol:        "6758",
		Price:         990.0,
		TradingVolume: 10600, // 出来高 +400 (10200 -> 10600)
		BuyBoard: []tick.Quote{
			{Price: 990, Qty: 300},
		},
		CurrentPriceTime: tickTime150,
	})

	orders, _ := g.GetOrders(context.Background())
	if orders.Orders[0].Status() != order.ORDER_STATUS_FILLED {
		t.Errorf("expected order to be filled via volume digest, got %v", orders.Orders[0].Status())
	}
}

func TestGateway_ShortCoverReproduction(t *testing.T) {
	// 遅延なし、価格モデル
	g := NewBacktestGateway(ExecutionModelPrice, 0)
	baseTime := time.Now()

	// 1. 新規空売りを発注 (7201, 売り, 信用新規)
	_, err := g.SendOrder(context.Background(), order.SendOrderInput{
		Order: &order.Order{
			Symbol:     "7201",
			Action:     order.ACTION_SELL,
			OrderQty:   100,
			OrderPrice: 400.0,
			CashMargin: order.CASH_MARGIN_MARGIN_ENTRY,
			Type:       order.ORDER_TYPE_LIMIT,
		},
	})
	if err != nil {
		t.Fatalf("SendOrder failed: %v", err)
	}

	// 2. 約定させて「売り建玉」を持たせる
	g.ProcessTick(tick.Tick{
		Symbol:            "7201",
		Price:             400.0,
		CurrentPriceTime: baseTime,
	})

	positions, _ := g.GetPositions(context.Background(), order.PRODUCT_MARGIN)
	if len(positions) != 1 || positions[0].Action != order.ACTION_SELL {
		t.Fatalf("expected 1 short position, got %+v", positions)
	}

	// 3. すでに売り建玉がある状態で、新規買い注文（両建て）を送信しようとする
	// 両建て規制によりエラーになるはず
	_, err = g.SendOrder(context.Background(), order.SendOrderInput{
		Order: &order.Order{
			Symbol:     "7201",
			Action:     order.ACTION_BUY,
			OrderQty:   100,
			OrderPrice: 395.0,
			CashMargin: order.CASH_MARGIN_MARGIN_ENTRY,
			Type:       order.ORDER_TYPE_LIMIT,
		},
	})
	if err == nil {
		t.Errorf("expected error due to double position restriction, but got nil")
	}

	// 4. 正しい返済注文（CashMargin: 3）を送信
	// 口座に売り建玉があるため、これは成功するはず
	_, err = g.SendOrder(context.Background(), order.SendOrderInput{
		Order: &order.Order{
			Symbol:     "7201",
			Action:     order.ACTION_BUY,
			OrderQty:   100,
			OrderPrice: 395.0,
			CashMargin: order.CASH_MARGIN_MARGIN_EXIT,
			Type:       order.ORDER_TYPE_LIMIT,
		},
	})
	if err != nil {
		t.Errorf("expected success for correct exit order, but got error: %v", err)
	}
}

type MockSniperStrategy struct {
	hasPosition bool
}

func (m *MockSniperStrategy) Name() string { return "mock_sniper_strategy" }
func (m *MockSniperStrategy) Evaluate(input strategy.StrategyInput) strategy.TargetPosition {
	if !m.hasPosition {
		// 最初は空売りエントリー
		return strategy.TargetPosition{
			Qty:       -100,
			Price:     400.0,
			OrderType: order.ORDER_TYPE_LIMIT,
			Reason:    "PairEntry_SellA",
		}
	} else {
		// エントリー後は買い戻し決済
		return strategy.TargetPosition{
			Qty:       0,
			Price:     395.0,
			OrderType: order.ORDER_TYPE_LIMIT,
			Reason:    "PairExit_BuyA",
		}
	}
}
func (m *MockSniperStrategy) AnalysisLogger() *slog.Logger { return nil }

func TestSniper_ShortCover_TDD_Verification(t *testing.T) {
	g := NewBacktestGateway(ExecutionModelPrice, 0)
	detail := symbol.Symbol{Code: "7201"}
	strat := &MockSniperStrategy{}
	s := sniper.NewSniper("test-sniper", detail, strat, &strategy.NoopPolicy{}, order.EXCHANGE_TOSHO, nil)
	nest := sniper.NewSniperNest("7201", detail, []*sniper.Sniper{s}, nil)

	baseTime := time.Now()

	// ---- 1. 最初のエントリー（新規空売り）の評価と発注 ----
	obs1 := sniper.Observation{
		Tick: tick.Tick{Price: 400.0, CurrentPriceTime: baseTime},
	}
	input1 := strategy.StrategyInput{
		Position:   obs1.CalculateVirtualPosition(),
		LatestTick: obs1.Tick,
	}
	target1 := s.Evaluate(input1)
	bullet1 := nest.ReconcileTarget(s.ID, obs1.Tick, target1, s.Exchange, s.MarginTradeType, s.AccountType, s.ExecutionPolicy)

	ordBullet1, ok1 := bullet1.(sniper.OrderBullet)
	if !ok1 {
		t.Fatalf("expected entry order to be generated")
	}

	nest.AddOrder(s.ID, ordBullet1.Order)

	_, err := g.SendOrder(context.Background(), order.SendOrderInput{Order: ordBullet1.Order})
	if err != nil {
		t.Fatalf("🚨 【バグ再現】新規空売りの発注に失敗しました。新規注文が返済注文に誤変換されています: %v", err)
	}

	// 注文約定（ポジション保有）
	g.ProcessTick(tick.Tick{
		Symbol:            "7201",
		Price:             400.0,
		CurrentPriceTime:  baseTime.Add(time.Second),
		CurrentPriceStatus: 1,
	})

	// 約定結果を spotter に同期する
	ordersReport, _ := g.GetOrders(context.Background())
	nest.Update(ordersReport, baseTime.Add(time.Second))

	// 戦略の状態を「ポジションあり」に更新
	strat.hasPosition = true

	// ---- 2. 決済（買い戻し）の評価と発注 ----
	obs2 := nest.PrepareObservation(s.ID, tick.Tick{Price: 395.0, CurrentPriceTime: baseTime.Add(2 * time.Second)}, s.ExecutionPolicy)
	input2 := strategy.StrategyInput{
		Position:   obs2.CalculateVirtualPosition(),
		LatestTick: obs2.Tick,
	}
	target2 := s.Evaluate(input2)
	bullet2 := nest.ReconcileTarget(s.ID, obs2.Tick, target2, s.Exchange, s.MarginTradeType, s.AccountType, s.ExecutionPolicy)

	ordBullet2, ok2 := bullet2.(sniper.OrderBullet)
	if !ok2 {
		t.Fatalf("expected exit order to be generated")
	}

	_, err2 := g.SendOrder(context.Background(), order.SendOrderInput{Order: ordBullet2.Order})
	if err2 != nil {
		t.Fatalf("🚨 【バグ再現】買い戻し決済の発注に失敗しました。決済注文が新規注文に誤変換されています: %v", err2)
	}
}
