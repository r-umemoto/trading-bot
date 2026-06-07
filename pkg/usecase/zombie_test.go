package usecase

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
	"github.com/r-umemoto/trading-bot/pkg/infra/backtest"
	"github.com/r-umemoto/trading-bot/pkg/infra/kabu/api"
)

func TestTradeUseCase_ZombieOrderReconciliation(t *testing.T) {
	// 1. バックテスト用ゲートウェイの生成 (遅延 100ms)
	g := backtest.NewSyncBacktestGateway(backtest.ExecutionModelTouch, 100*time.Millisecond)

	// 2. スナイパーと作戦の構築
	detail := symbol.Symbol{Code: "7201"}
	strategyA := sniper.NewInstructionStrategy()
	policy := &strategy.TouchTTLPolicy{TTL: 2000 * time.Millisecond}
	s := sniper.NewSniper("test_sniper_7201", detail, strategyA, policy, order.EXCHANGE_TOSHO, nil)
	nest := sniper.NewSniperNest("7201", detail, []*sniper.Sniper{s}, nil)
	op := sniper.NewDefaultOperation("Op_7201", nest)

	// 3. TradeUseCase の生成
	u := NewTradeUseCase([]sniper.Operation{op}, g, nil)

	// 4. ベース時刻の設定と注文の送信
	baseTime := time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)
	g.SetTime(baseTime)

	ord := order.NewOrder("local-123", "7201", order.ACTION_BUY, 1000.0, 100)
	ord.Reason = "EmergencyExit" // 緊急決済を模してタイムアウトを2秒に設定
	
	ord.Type = order.ORDER_TYPE_LIMIT
	sendInput := order.SendOrderInput{
		Order: ord,
	}
	
	_, err := g.SendOrder(context.Background(), sendInput)
	if err != nil {
		t.Fatalf("SendOrder failed: %v", err)
	}

	// 注文IDの更新と管理登録
	ord.ID = "bt_order_1"
	ord.BypassTransition(order.ORDER_STATUS_IN_PROGRESS, order.STATE_ACTIVE)

	ordInSniper := *ord
	nest.AddOrder(s.ID, &ordInSniper)

	// 5. 注文キャンセル要求を送信
	err = g.CancelOrder(context.Background(), "bt_order_1")
	if err != nil {
		t.Fatalf("CancelOrder failed: %v", err)
	}

	// キャンセル送信中ステータスにして、送信時刻を記録
	ordInSniper.BypassTransition(order.ORDER_STATUS_CANCEL_SENT, ordInSniper.InternalState())
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
	if ordInSniper.Status() != order.ORDER_STATUS_CANCEL_SENT {
		t.Errorf("expected local status to remain CANCEL_SENT, got %v", ordInSniper.Status())
	}

	// 7. 照会同期（GetOrders）を手動で呼び出して自己修復を即時実行
	u.reconcileZombieOrder(context.Background(), op)

	// 8. 照会（GetOrders）後のステータスを確認
	// 自己修復により、ローカルステータスが CANCELED に更新されているはず
	// （※ Spotter内で更新されたはずの注文インスタンスを調べるため、Spotterから注文を取得する）
	activeOrders := nest.GetSniperActiveOrders(s.ID)
	if len(activeOrders) != 0 {
		t.Errorf("expected reconciled order to be completed and removed from active list, but got %d orders", len(activeOrders))
	}
}

type faultInjectingGateway struct {
	market.MarketGateway
	failSend bool
	err      error
}

func (f *faultInjectingGateway) SendOrder(ctx context.Context, input order.SendOrderInput) (*order.Order, error) {
	if f.failSend {
		// input.Order のコピーを作成して渡すことで、バックテスト用ゲートウェイによるポインタ書き換えの影響を防ぎます
		clonedOrd := *input.Order
		clonedInput := order.SendOrderInput{
			Order: &clonedOrd,
		}
		_, _ = f.MarketGateway.SendOrder(ctx, clonedInput)
		if f.err != nil {
			return input.Order, f.err
		}
		return input.Order, fmt.Errorf("カブコムAPI発注失敗: タイムアウトエラー(5秒)")
	}
	return f.MarketGateway.SendOrder(ctx, input)
}

func TestTradeUseCase_SendOrderTimeoutReconciliation(t *testing.T) {
	// 1. バックテスト用ゲートウェイの生成 (遅延 100ms)
	g := backtest.NewSyncBacktestGateway(backtest.ExecutionModelTouch, 100*time.Millisecond)

	// 障害注入用のデコレータを用意
	fg := &faultInjectingGateway{MarketGateway: g, failSend: true}

	// 2. スナイパーと作戦の構築
	detail := symbol.Symbol{Code: "7201"}
	strategyA := sniper.NewInstructionStrategy()
	policy := &strategy.TouchTTLPolicy{TTL: 2000 * time.Millisecond}
	s := sniper.NewSniper("test_sniper_7201", detail, strategyA, policy, order.EXCHANGE_TOSHO, nil)
	nest := sniper.NewSniperNest("7201", detail, []*sniper.Sniper{s}, nil)
	op := sniper.NewDefaultOperation("Op_7201", nest)

	// 3. TradeUseCase の生成
	u := NewTradeUseCase([]sniper.Operation{op}, fg, nil)

	// 4. ベース時刻の設定と注文の送信
	baseTime := time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)
	g.SetTime(baseTime)

	ord := order.NewOrder("local-123", "7201", order.ACTION_BUY, 1000.0, 100)

	// スナイパーのActiveOrdersに仮の注文として追加 (火を入れる前の状態)
	nest.AddOrder(s.ID, ord)

	ord.Type = order.ORDER_TYPE_LIMIT
	bullet := sniper.OrderBullet{
		Order: ord,
	}

	// 5. fire を実行。SendOrder はエラーを返し、注文は一時的に墓標（Tombstone）に入るため、アクティブ注文からは消えるはず
	u.fire(context.Background(), op, s.ID, bullet)

	// s.ActiveOrders が一旦空になっていることを確認
	activeOrders := nest.GetSniperActiveOrders(s.ID)
	if len(activeOrders) != 0 {
		t.Fatalf("expected 0 active orders immediately after fire failure (should be tombstoned), got %d", len(activeOrders))
	}

	// 6. 取引所の注文状態を取得し、非同期の定期更新 (UpdateOrders) を実行する
	// 取引所側には注文が受理されているので、GetOrdersで取得したものを流し込む
	ords, err := fg.GetOrders(context.Background())
	if err != nil {
		t.Fatalf("GetOrders failed: %v", err)
	}

	op.UpdateOrders(ords)

	// 7. 復旧後のアクティブ注文を確認。墓標から復活し、IDがサーバー側 (bt_order_1) に書き換わっていることを確認
	activeOrders = nest.GetSniperActiveOrders(s.ID)
	if len(activeOrders) != 1 {
		t.Fatalf("expected 1 active order after resurrection, got %d", len(activeOrders))
	}

	o := activeOrders[0]
	if o.ID != "bt_order_1" {
		t.Errorf("expected reconciled order ID to be updated to 'bt_order_1', got %s", o.ID)
	}

	if o.InternalState() != order.STATE_ACTIVE {
		t.Errorf("expected internal state to be STATE_ACTIVE, got %v", o.InternalState())
	}
}

func TestTradeUseCase_SendOrderPermanentErrorBypassReconciliation(t *testing.T) {
	// 1. バックテスト用ゲートウェイの生成 (遅延 100ms)
	g := backtest.NewSyncBacktestGateway(backtest.ExecutionModelTouch, 100*time.Millisecond)

	// 障害注入用のデコレータを用意 (Code 8: 決済指定内容誤り を再現)
	fg := &faultInjectingGateway{
		MarketGateway: g,
		failSend:      true,
		err: &api.KabuAPIError{
			StatusCode: 500,
			Code:       8,
			Message:    "決済指定内容に誤りがあります",
		},
	}

	// 2. スナイパーと作戦の構築
	detail := symbol.Symbol{Code: "7201"}
	strategyA := sniper.NewInstructionStrategy()
	policy := &strategy.TouchTTLPolicy{TTL: 2000 * time.Millisecond}
	s := sniper.NewSniper("test_sniper_7201", detail, strategyA, policy, order.EXCHANGE_TOSHO, nil)
	nest := sniper.NewSniperNest("7201", detail, []*sniper.Sniper{s}, nil)
	op := sniper.NewDefaultOperation("Op_7201", nest)

	// 3. TradeUseCase の生成
	u := NewTradeUseCase([]sniper.Operation{op}, fg, nil)

	// 4. ベース時刻の設定と注文の送信
	baseTime := time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)
	g.SetTime(baseTime)

	ord := order.NewOrder("local-123", "7201", order.ACTION_BUY, 1000.0, 100)

	// スナイパーのActiveOrdersに仮の注文として追加
	nest.AddOrder(s.ID, ord)

	ord.Type = order.ORDER_TYPE_LIMIT
	bullet := sniper.OrderBullet{
		Order: ord,
	}

	// 5. fire を実行。SendOrder は恒久エラーを返したため、即失敗としてアクティブリストから削除されるはず（照合バイパス）
	u.fire(context.Background(), op, s.ID, bullet)

	// s.ActiveOrders が空になっている（Reconciliationされずに即削除された）ことを確認
	activeOrders := nest.GetSniperActiveOrders(s.ID)
	if len(activeOrders) != 0 {
		t.Fatalf("expected 0 active orders (immediately failed), got %d", len(activeOrders))
	}
}
