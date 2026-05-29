package usecase

import (
	"context"
	"testing"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
	"github.com/r-umemoto/trading-bot/pkg/infra/backtest"
)

func TestTradeUseCase_ZombieOrderReconciliation(t *testing.T) {
	// 1. バックテスト用ゲートウェイの生成 (遅延 100ms)
	g := backtest.NewSyncBacktestGateway(backtest.ExecutionModelTouch, 100*time.Millisecond)

	// 2. スナイパーと作戦の構築
	detail := symbol.Symbol{Code: "7201"}
	strategyA := sniper.NewInstructionStrategy()
	policy := &strategy.TouchTTLPolicy{TTL: 2000 * time.Millisecond}
	s := sniper.NewSniper("test_sniper_7201", detail, strategyA, policy, order.EXCHANGE_TOSHO, nil)
	nest := sniper.NewSniperNest("7201", sniper.NewSpotter(detail, nil), []*sniper.Sniper{s})
	op := sniper.NewDefaultOperation("Op_7201", nest)

	// 3. TradeUseCase の生成
	u := NewTradeUseCase([]sniper.Operation{op}, g)

	// 4. ベース時刻の設定と注文の送信
	baseTime := time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)
	g.SetTime(baseTime)

	ord := order.NewOrder("local-123", "7201", order.ACTION_BUY, 1000.0, 100)
	ord.Reason = "EmergencyExit" // 緊急決済を模してタイムアウトを2秒に設定
	
	sendInput := order.SendOrderInput{
		Order: ord,
		Request: order.OrderRequest{
			OrderType: order.ORDER_TYPE_LIMIT,
		},
	}
	
	_, err := g.SendOrder(context.Background(), sendInput)
	if err != nil {
		t.Fatalf("SendOrder failed: %v", err)
	}

	// 注文IDの更新と管理登録
	ord.ID = "bt_order_1"
	ord.InternalState = order.STATE_ACTIVE
	ord.Status = order.ORDER_STATUS_IN_PROGRESS

	ordInSniper := *ord
	s.ActiveOrders = append(s.ActiveOrders, &ordInSniper)

	// 5. 注文キャンセル要求を送信
	err = g.CancelOrder(context.Background(), "bt_order_1")
	if err != nil {
		t.Fatalf("CancelOrder failed: %v", err)
	}

	// キャンセル送信中ステータスにして、送信時刻を記録
	ordInSniper.Status = order.ORDER_STATUS_CANCEL_SENT
	ordInSniper.CancelSentAt = baseTime

	// 🌟 【障害注入（Fault Injection）】: キャンセル完了通知（イベント）をロストさせる
	g.InjectCancelSilentFault("bt_order_1")

	// 6. 時間を3秒進めて、キャンセル到達＆タイムアウト条件を満たす
	futureTime := baseTime.Add(3 * time.Second)
	g.ProcessTick(tick.Tick{
		Symbol:            "7201",
		Price:             1000.0,
		CurrentPriceTime: futureTime,
	})

	// この時点で、取引所（gateway内部）では注文はCANCELEDになっているが、
	// イベント通知が障害注入によりロストしたため、ボットのローカルステータスは依然として CANCEL_SENT のまま（膠着状態＝ゾンビ化）
	if ordInSniper.Status != order.ORDER_STATUS_CANCEL_SENT {
		t.Errorf("expected local status to remain CANCEL_SENT, got %v", ordInSniper.Status)
	}

	// 7. 照会同期（GetOrders）を手動で呼び出して自己修復を即時実行
	u.reconcileZombieOrder(context.Background(), op)

	// 8. 照会（GetOrders）後のステータスを確認
	// 自己修復により、ローカルステータスが CANCELED に更新されているはず
	if ordInSniper.Status != order.ORDER_STATUS_CANCELED {
		t.Errorf("expected zombie self-healing to resolve status to CANCELED, but got %v", ordInSniper.Status)
	}
}
