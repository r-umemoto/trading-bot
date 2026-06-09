package order

import (
	"testing"
	"time"
)

func TestReconcileOrders_PendingOrdersTimeout(t *testing.T) {
	now := time.Now()

	// 30秒未満の Pending 注文
	o1 := NewOrder("local-1", "7203", ACTION_BUY, 2000, 100)
	o1.CreatedAt = now.Add(-10 * time.Second)

	// 30秒以上の Pending 注文 (タイムアウトで除外されるべき)
	o2 := NewOrder("local-2", "7203", ACTION_BUY, 2000, 100)
	o2.CreatedAt = now.Add(-40 * time.Second)

	// アクティブ（送信完了・執行中）な注文
	o3 := NewOrder("api-3", "7203", ACTION_BUY, 2000, 100)
	o3.BypassTransition(ORDER_STATUS_IN_PROGRESS, STATE_ACTIVE)

	localOrders := []*Order{o1, o2, o3}
	apiOrders := Orders{Orders: []Order{}}
	processedExecs := make(map[string]bool)

	reconciled, execs := ReconcileOrders(localOrders, apiOrders, "7203", processedExecs, now)

	if len(execs) != 0 {
		t.Errorf("expected 0 executions, got %d", len(execs))
	}

	// o2 がタイムアウトし、o1 と o3 が残るはず
	if len(reconciled) != 2 {
		t.Fatalf("expected 2 reconciled orders, got %d", len(reconciled))
	}

	if reconciled[0].ID != "local-1" || reconciled[1].ID != "api-3" {
		t.Errorf("unexpected reconciled orders order or IDs: %+v", reconciled)
	}
}

func TestReconcileOrders_SyncStatusAndExecutions(t *testing.T) {
	now := time.Now()

	// ローカルのアクティブ注文
	o1 := NewOrder("order-1", "7203", ACTION_BUY, 2000, 100)
	o1.BypassTransition(ORDER_STATUS_IN_PROGRESS, STATE_ACTIVE)

	localOrders := []*Order{o1}

	// APIからの更新レポート（部分約定と新規約定が発生）
	apiOrder := Order{
		ID:         "order-1",
		Symbol:     "7203",
		Action:     ACTION_BUY,
		OrderPrice: 2000,
		OrderQty:   100,
		status:     ORDER_STATUS_IN_PROGRESS,
		CumQty:     40,
		Executions: []Execution{
			{ID: "exec-2", Price: 2000, Qty: 10, ExecutionTime: now.Add(-1 * time.Minute)},
			{ID: "exec-1", Price: 2000, Qty: 30, ExecutionTime: now.Add(-2 * time.Minute)}, // 時系列でこちらが先
		},
	}

	apiOrders := Orders{Orders: []Order{apiOrder}}

	// すでに "exec-1" は処理済みと仮定
	processedExecs := map[string]bool{
		"exec-1": true,
	}

	reconciled, execs := ReconcileOrders(localOrders, apiOrders, "7203", processedExecs, now)

	// ステータスと約定数量が同期されていること
	if o1.CumQty != 40 {
		t.Errorf("expected CumQty 40, got %f", o1.CumQty)
	}
	if o1.Status() != ORDER_STATUS_IN_PROGRESS {
		t.Errorf("expected status IN_PROGRESS, got %v", o1.Status())
	}

	// 保持されていること
	if len(reconciled) != 1 || reconciled[0].ID != "order-1" {
		t.Errorf("expected order-1 to be reconciled")
	}

	// 未処理の "exec-2" のみが抽出されていること
	if len(execs) != 1 {
		t.Fatalf("expected 1 execution, got %d", len(execs))
	}
	if execs[0].Execution.ID != "exec-2" {
		t.Errorf("expected execution exec-2, got %s", execs[0].Execution.ID)
	}
}

func TestReconcileOrders_ExecutionSorting(t *testing.T) {
	now := time.Now()

	o1 := NewOrder("order-1", "7203", ACTION_BUY, 2000, 100)
	o1.BypassTransition(ORDER_STATUS_IN_PROGRESS, STATE_ACTIVE)

	localOrders := []*Order{o1}

	apiOrder := Order{
		ID:         "order-1",
		Symbol:     "7203",
		Action:     ACTION_BUY,
		status:     ORDER_STATUS_FILLED,
		CumQty:     100,
		Executions: []Execution{
			{ID: "exec-c", Price: 2000, Qty: 30, ExecutionTime: now.Add(-10 * time.Second)}, // 3番目
			{ID: "exec-a", Price: 2000, Qty: 40, ExecutionTime: now.Add(-30 * time.Second)}, // 1番目
			{ID: "exec-b", Price: 2000, Qty: 30, ExecutionTime: now.Add(-20 * time.Second)}, // 2番目
		},
	}

	apiOrders := Orders{Orders: []Order{apiOrder}}
	processedExecs := make(map[string]bool)

	_, execs := ReconcileOrders(localOrders, apiOrders, "7203", processedExecs, now)

	if len(execs) != 3 {
		t.Fatalf("expected 3 executions, got %d", len(execs))
	}

	// 時系列順 (a -> b -> c) にソートされていること
	if execs[0].Execution.ID != "exec-a" || execs[1].Execution.ID != "exec-b" || execs[2].Execution.ID != "exec-c" {
		t.Errorf("executions are not sorted by execution time: %+v", execs)
	}
}

func TestReconcileOrders_SymbolMismatchAndNoMatch(t *testing.T) {
	now := time.Now()

	o1 := NewOrder("order-1", "7203", ACTION_BUY, 2000, 100)
	o1.BypassTransition(ORDER_STATUS_IN_PROGRESS, STATE_ACTIVE)

	localOrders := []*Order{o1}

	apiOrders := Orders{Orders: []Order{
		// 異なるシンボルの注文 (無視されるべき)
		{
			ID:     "order-1",
			Symbol: "9999",
			status: ORDER_STATUS_FILLED,
			CumQty: 100,
		},
		// 一致するシンボルだが、どのローカル注文ともマッチしない注文 (無視されるべき)
		{
			ID:     "order-unknown",
			Symbol: "7203",
			status: ORDER_STATUS_FILLED,
			CumQty: 100,
		},
	}}

	processedExecs := make(map[string]bool)
	reconciled, execs := ReconcileOrders(localOrders, apiOrders, "7203", processedExecs, now)

	if len(reconciled) != 1 || reconciled[0].ID != "order-1" {
		t.Errorf("expected order-1 to remain active")
	}
	if reconciled[0].Status() != ORDER_STATUS_IN_PROGRESS {
		t.Errorf("expected order-1 status to remain IN_PROGRESS, got %v", reconciled[0].Status())
	}
	if len(execs) != 0 {
		t.Errorf("expected 0 executions, got %d", len(execs))
	}
}

func TestReconcileOrders_MatchOutsideActive(t *testing.T) {
	now := time.Now()

	// 完了済みのローカル注文（アクティブな注文リストからは事前に除外されるはず）
	o1 := NewOrder("order-1", "7203", ACTION_BUY, 2000, 100)
	o1.BypassTransition(ORDER_STATUS_FILLED, STATE_CLOSED)

	localOrders := []*Order{o1}

	// APIからは完了した注文情報が届く
	apiOrder := Order{
		ID:     "order-1",
		Symbol: "7203",
		status: ORDER_STATUS_FILLED,
		CumQty: 100,
	}

	apiOrders := Orders{Orders: []Order{apiOrder}}
	processedExecs := make(map[string]bool)

	reconciled, _ := ReconcileOrders(localOrders, apiOrders, "7203", processedExecs, now)

	// reconciled はアクティブな（未完了の）注文のみを返すので、完了した o1 は含まれない
	if len(reconciled) != 0 {
		t.Errorf("expected 0 reconciled active orders, got %d", len(reconciled))
	}
}

func TestReconcileOrders_SpecialStateSync(t *testing.T) {
	now := time.Now()

	// 1. FillExpected (疑似約定) の状態で、API側がまだ未完了 (IN_PROGRESS) の場合
	o1 := NewOrder("order-1", "7203", ACTION_BUY, 2000, 100)
	o1.BypassTransition(ORDER_STATUS_FILL_EXPECTED, STATE_ACTIVE)

	// 2. CancelSent の状態で、API側がまだ未完了 (IN_PROGRESS) の場合
	o2 := NewOrder("order-2", "7203", ACTION_BUY, 2000, 100)
	o2.BypassTransition(ORDER_STATUS_CANCEL_SENT, STATE_ACTIVE)

	// 3. Pending 状態 (発注中) の場合、API側で検知されたら ACTIVE に移行する
	o3 := NewOrder("order-3", "7203", ACTION_BUY, 2000, 100)
	o3.BypassTransition(ORDER_STATUS_WAITING, STATE_PENDING)

	// 4. FillExpected の状態で、API側が完了 (FILLED) になった場合
	o4 := NewOrder("order-4", "7203", ACTION_BUY, 2000, 100)
	o4.BypassTransition(ORDER_STATUS_FILL_EXPECTED, STATE_ACTIVE)

	// 5. CancelSent の状態で、API側が完了 (CANCELED) になった場合
	o5 := NewOrder("order-5", "7203", ACTION_BUY, 2000, 100)
	o5.BypassTransition(ORDER_STATUS_CANCEL_SENT, STATE_ACTIVE)

	localOrders := []*Order{o1, o2, o3, o4, o5}

	apiOrders := Orders{Orders: []Order{
		{
			ID:     "order-1",
			Symbol: "7203",
			status: ORDER_STATUS_IN_PROGRESS,
			CumQty: 50,
		},
		{
			ID:     "order-2",
			Symbol: "7203",
			status: ORDER_STATUS_IN_PROGRESS,
			CumQty: 0,
		},
		{
			ID:     "order-3",
			Symbol: "7203",
			status: ORDER_STATUS_IN_PROGRESS,
			CumQty: 0,
		},
		{
			ID:     "order-4",
			Symbol: "7203",
			status: ORDER_STATUS_FILLED,
			CumQty: 100,
		},
		{
			ID:     "order-5",
			Symbol: "7203",
			status: ORDER_STATUS_CANCELED,
			CumQty: 0,
		},
	}}

	processedExecs := make(map[string]bool)
	reconciled, _ := ReconcileOrders(localOrders, apiOrders, "7203", processedExecs, now)

	// アクティブ注文リストには未完了の注文 (o1, o2, o3) のみが残る (o4, o5 は完了したので除外される)
	if len(reconciled) != 3 {
		t.Fatalf("expected 3 reconciled orders, got %d", len(reconciled))
	}

	// o1: FillExpected の状態が維持され、CumQty が同期されていること
	if o1.Status() != ORDER_STATUS_FILL_EXPECTED {
		t.Errorf("expected status to remain FILL_EXPECTED, got %v", o1.Status())
	}
	if o1.CumQty != 50 {
		t.Errorf("expected CumQty to be 50, got %f", o1.CumQty)
	}

	// o2: CancelSent の状態が維持されていること
	if o2.Status() != ORDER_STATUS_CANCEL_SENT {
		t.Errorf("expected status to remain CANCEL_SENT, got %v", o2.Status())
	}

	// o3: InternalState が PENDING から ACTIVE に遷移していること
	if o3.InternalState() != STATE_ACTIVE {
		t.Errorf("expected internal state to transition to ACTIVE, got %v", o3.InternalState())
	}

	// o4: status が FILLED に更新されていること
	if o4.Status() != ORDER_STATUS_FILLED {
		t.Errorf("expected status to transition to FILLED, got %v", o4.Status())
	}

	// o5: status が CANCELED に更新されていること
	if o5.Status() != ORDER_STATUS_CANCELED {
		t.Errorf("expected status to transition to CANCELED, got %v", o5.Status())
	}
}
