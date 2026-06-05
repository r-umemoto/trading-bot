package kabu

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
)

type MockSender struct {
	mu          sync.Mutex
	SentOrders  []order.SendOrderInput
	CancelIDs   []string
	SendErr     error
	CancelErr   error
	SendLatency time.Duration
}

func (m *MockSender) SendOrderRaw(ctx context.Context, input order.SendOrderInput) (*order.Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.SendLatency > 0 {
		time.Sleep(m.SendLatency)
	}
	m.SentOrders = append(m.SentOrders, input)
	if m.SendErr != nil {
		return input.Order, m.SendErr
	}
	ret := *input.Order
	if ret.ID == "" || len(ret.ID) < 10 {
		ret.ID = "server-" + ret.ID
	}
	return &ret, nil
}

func (m *MockSender) CancelOrderRaw(ctx context.Context, orderID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.CancelIDs = append(m.CancelIDs, orderID)
	return m.CancelErr
}

func TestOrderDispatcher_SubmitKeys(t *testing.T) {
	sender := &MockSender{}
	dispatcher := NewOrderDispatcher(sender)

	// 1. 新規注文（エントリー）は異なるIDであれば上書きされない
	ord1 := order.NewOrder("local-entry-1", "4689", order.ACTION_BUY, 400.0, 100)
	ord1.Request = &order.OrderRequest{
		Exchange:        order.EXCHANGE_TOSHO,
		SecurityType:    order.SECURITY_TYPE_STOCK,
		MarginTradeType: order.TRADE_TYPE_GENERAL_DAY,
		AccountType:     order.ACCOUNT_SPECIAL,
	}
	ord2 := order.NewOrder("local-entry-2", "4689", order.ACTION_BUY, 400.0, 100)
	ord2.Request = &order.OrderRequest{
		Exchange:        order.EXCHANGE_TOSHO,
		SecurityType:    order.SECURITY_TYPE_STOCK,
		MarginTradeType: order.TRADE_TYPE_GENERAL_DAY,
		AccountType:     order.ACCOUNT_SPECIAL,
	}

	ch1 := dispatcher.Submit(ord1.ID, ord1.Symbol, ord1, "", 10)
	ch2 := dispatcher.Submit(ord2.ID, ord2.Symbol, ord2, "", 10)

	// どちらもキューに残っているはず（pendingJobs の長さが 2）
	dispatcher.jobMu.Lock()
	jobsLen := len(dispatcher.pendingJobs)
	dispatcher.jobMu.Unlock()

	if jobsLen != 2 {
		t.Errorf("expected 2 pending jobs for entries, got %d", jobsLen)
	}

	// 各チャネルがクローズされてエラーが送られていないか確認
	select {
	case res, ok := <-ch1:
		if ok && res.Error != nil {
			t.Errorf("ord1 was unexpectedly overwritten: %v", res.Error)
		}
	default:
	}
	select {
	case res, ok := <-ch2:
		if ok && res.Error != nil {
			t.Errorf("ord2 was unexpectedly overwritten: %v", res.Error)
		}
	default:
	}

	// 2. 同じ jobID を指定した場合は上書きされる
	exitOrd1 := order.NewOrder("local-exit-1", "4689", order.ACTION_SELL, 410.0, 100)
	exitOrd1.Request = &order.OrderRequest{
		Exchange:        order.EXCHANGE_TOSHO,
		SecurityType:    order.SECURITY_TYPE_STOCK,
		MarginTradeType: order.TRADE_TYPE_GENERAL_DAY,
		AccountType:     order.ACCOUNT_SPECIAL,
		ClosePositions: []order.ClosePosition{
			{HoldID: "exec-hold-id-123", Qty: 100},
		},
	}

	exitOrd2 := order.NewOrder("local-exit-2", "4689", order.ACTION_SELL, 410.0, 100)
	exitOrd2.Request = &order.OrderRequest{
		Exchange:        order.EXCHANGE_TOSHO,
		SecurityType:    order.SECURITY_TYPE_STOCK,
		MarginTradeType: order.TRADE_TYPE_GENERAL_DAY,
		AccountType:     order.ACCOUNT_SPECIAL,
		ClosePositions: []order.ClosePosition{
			{HoldID: "exec-hold-id-123", Qty: 100},
		},
	}

	// 一旦キューをクリアするために新しく作成
	dispatcher = NewOrderDispatcher(sender)

	exCh1 := dispatcher.Submit("dup-job-id", exitOrd1.Symbol, exitOrd1, "", 20)
	exCh2 := dispatcher.Submit("dup-job-id", exitOrd2.Symbol, exitOrd2, "", 20)

	dispatcher.jobMu.Lock()
	jobsLen = len(dispatcher.pendingJobs)
	dispatcher.jobMu.Unlock()

	if jobsLen != 1 {
		t.Errorf("expected 1 pending job due to deduplication, got %d", jobsLen)
	}

	// exCh1 は上書きエラーが送られてきているはず
	select {
	case res, ok := <-exCh1:
		if !ok {
			t.Error("exCh1 closed without result")
		} else if res.Error == nil || res.Error.Error() != "order overwritten in dispatch queue" {
			t.Errorf("expected overwrite error on exCh1, got: %v", res.Error)
		}
	default:
		t.Error("expected immediate error on exCh1, but blocked")
	}

	// exCh2 は上書きされていないはず
	select {
	case res := <-exCh2:
		t.Errorf("exCh2 should still be pending, but got: %v", res)
	default:
	}

	// 3. キャンセル注文で同じ jobID を指定した場合は上書きされる
	dispatcher = NewOrderDispatcher(sender)
	canCh1 := dispatcher.Submit("cancel_server-id-999", "4689", nil, "server-id-999", 30)
	canCh2 := dispatcher.Submit("cancel_server-id-999", "4689", nil, "server-id-999", 30)

	dispatcher.jobMu.Lock()
	jobsLen = len(dispatcher.pendingJobs)
	dispatcher.jobMu.Unlock()

	if jobsLen != 1 {
		t.Errorf("expected 1 pending job for cancels, got %d", jobsLen)
	}

	select {
	case res, ok := <-canCh1:
		if !ok {
			t.Error("canCh1 closed without result")
		} else if res.Error == nil || res.Error.Error() != "order overwritten in dispatch queue" {
			t.Errorf("expected overwrite error on canCh1, got: %v", res.Error)
		}
	default:
		t.Error("expected immediate error on canCh1, but blocked")
	}

	select {
	case res := <-canCh2:
		t.Errorf("canCh2 should still be pending, but got: %v", res)
	default:
	}
}

func TestOrderDispatcher_TwoStagePriority(t *testing.T) {
	sender := &MockSender{}
	dispatcher := NewOrderDispatcher(sender)

	// 1. 優先度テスト (Cancel: 30 > Exit: 20 > Entry: 10)
	// Entry 注文 (優先度 = 10)
	ordEntry := order.NewOrder("entry-1", "4689", order.ACTION_BUY, 400.0, 100)
	ordEntry.Request = &order.OrderRequest{
		Exchange:        order.EXCHANGE_TOSHO,
		SecurityType:    order.SECURITY_TYPE_STOCK,
		MarginTradeType: order.TRADE_TYPE_GENERAL_DAY,
		AccountType:     order.ACCOUNT_SPECIAL,
	}

	// Exit 注文 (優先度 = 20)
	ordExit := order.NewOrder("exit-1", "4689", order.ACTION_SELL, 410.0, 100)
	ordExit.Request = &order.OrderRequest{
		Exchange:        order.EXCHANGE_TOSHO,
		SecurityType:    order.SECURITY_TYPE_STOCK,
		MarginTradeType: order.TRADE_TYPE_GENERAL_DAY,
		AccountType:     order.ACCOUNT_SPECIAL,
		ClosePositions: []order.ClosePosition{
			{HoldID: "hold-a", Qty: 100},
		},
	}

	// Cancel 注文 (優先度 = 30)
	// Entry, Exit, Cancel を順番をシャッフルして登録
	_ = dispatcher.Submit(ordEntry.ID, ordEntry.Symbol, ordEntry, "", 10)
	_ = dispatcher.Submit(ordExit.ID, ordExit.Symbol, ordExit, "", 20)
	_ = dispatcher.Submit("cancel_server-order-to-cancel", "4689", nil, "server-order-to-cancel", 30)

	// ポップされる順番を確認 (Cancel -> Exit -> Entry であるべき)
	best1 := dispatcher.pickBestJob()
	if best1 == nil || best1.OrderID != "server-order-to-cancel" {
		t.Errorf("expected Cancel job first, got: %+v", best1)
	}

	best2 := dispatcher.pickBestJob()
	if best2 == nil || best2.OrderPtr.ID != "exit-1" {
		t.Errorf("expected Exit job second, got: %+v", best2)
	}

	best3 := dispatcher.pickBestJob()
	if best3 == nil || best3.OrderPtr.ID != "entry-1" {
		t.Errorf("expected Entry job last, got: %+v", best3)
	}

	// 2. 優先度順のテスト (優先度の高いものが優先され、同じなら先に要求されたものが優先)
	dispatcher = NewOrderDispatcher(sender)

	// 銘柄 A の通常 Entry 注文 (優先度 = 10)
	ordA := order.NewOrder("entry-A", "4689", order.ACTION_BUY, 400.0, 100)
	ordA.Request = &order.OrderRequest{
		Exchange:        order.EXCHANGE_TOSHO,
		SecurityType:    order.SECURITY_TYPE_STOCK,
		MarginTradeType: order.TRADE_TYPE_GENERAL_DAY,
		AccountType:     order.ACCOUNT_SPECIAL,
	}

	// 銘柄 B の緊急 Entry 注文 (優先度 = 25)
	ordB := order.NewOrder("entry-B", "5020", order.ACTION_SELL, 600.0, 100)
	ordB.Reason = "ForceExit"
	ordB.Request = &order.OrderRequest{
		Exchange:        order.EXCHANGE_TOSHO,
		SecurityType:    order.SECURITY_TYPE_STOCK,
		MarginTradeType: order.TRADE_TYPE_GENERAL_DAY,
		AccountType:     order.ACCOUNT_SPECIAL,
	}

	// 登録 (A を先に登録し、後から優先度の高い B を登録)
	_ = dispatcher.Submit(ordA.ID, ordA.Symbol, ordA, "", 10)
	_ = dispatcher.Submit(ordB.ID, ordB.Symbol, ordB, "", 25)

	// 順番の確認 (B が優先されるべき)
	first := dispatcher.pickBestJob()
	if first == nil || first.OrderPtr.ID != "entry-B" {
		t.Errorf("expected B (emergency) to be dispatched first, got: %+v", first)
	}

	second := dispatcher.pickBestJob()
	if second == nil || second.OrderPtr.ID != "entry-A" {
		t.Errorf("expected A to be dispatched second, got: %+v", second)
	}
}

func TestOrderDispatcher_DispatchWorker(t *testing.T) {
	sender := &MockSender{SendLatency: 5 * time.Millisecond}
	dispatcher := NewOrderDispatcher(sender)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dispatcher.Start(ctx)

	ord := order.NewOrder("local-1", "4689", order.ACTION_BUY, 400.0, 100)
	ord.Request = &order.OrderRequest{
		Exchange:        order.EXCHANGE_TOSHO,
		SecurityType:    order.SECURITY_TYPE_STOCK,
		MarginTradeType: order.TRADE_TYPE_GENERAL_DAY,
		AccountType:     order.ACCOUNT_SPECIAL,
	}

	ch := dispatcher.Submit(ord.ID, ord.Symbol, ord, "", 10)

	select {
	case res := <-ch:
		if res.Error != nil {
			t.Fatalf("dispatch failed: %v", res.Error)
		}
		if res.OrderID != "server-local-1" {
			t.Errorf("expected order ID updated to server-local-1, got %s", res.OrderID)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for dispatch")
	}

	sender.mu.Lock()
	sentLen := len(sender.SentOrders)
	sender.mu.Unlock()

	if sentLen != 1 {
		t.Errorf("expected 1 sent order, got %d", sentLen)
	}
}
