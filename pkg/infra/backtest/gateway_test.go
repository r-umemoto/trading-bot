package backtest

import (
	"context"
	"testing"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
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
		},
		Request: order.OrderRequest{
			OrderType: order.ORDER_TYPE_LIMIT,
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
	if orders.Orders[0].Status == order.ORDER_STATUS_FILLED {
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
	if orders.Orders[0].Status != order.ORDER_STATUS_FILLED {
		t.Errorf("expected order to be filled at 100ms, but got: %v", orders.Orders[0].Status)
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
		},
		Request: order.OrderRequest{
			OrderType: order.ORDER_TYPE_LIMIT,
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
	if orders.Orders[0].Status != order.ORDER_STATUS_CANCEL_SENT {
		t.Errorf("expected status ORDER_STATUS_CANCEL_SENT, got %v", orders.Orders[0].Status)
	}

	// 10:00:00.150 (キャンセル到達前の価格タッチ。約定が優先されるはず)
	tickTime150, _ := time.Parse("15:04:05.000", "10:00:00.150")
	g.ProcessTick(tick.Tick{
		Symbol:            "6758",
		Price:             990.0,
		CurrentPriceTime: tickTime150,
	})

	orders, _ = g.GetOrders(context.Background())
	if orders.Orders[0].Status != order.ORDER_STATUS_FILLED {
		t.Errorf("expected order to be filled before cancel reaches exchange, but got %v", orders.Orders[0].Status)
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
	if orders.Orders[0].Status != order.ORDER_STATUS_FILLED {
		t.Errorf("expected order to remain FILLED, but got %v", orders.Orders[0].Status)
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
		},
		Request: order.OrderRequest{
			OrderType: order.ORDER_TYPE_LIMIT,
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
	if orders.Orders[0].Status != order.ORDER_STATUS_FILLED {
		t.Errorf("expected order to be filled via volume digest, got %v", orders.Orders[0].Status)
	}
}
