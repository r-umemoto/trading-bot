package engine

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type scheduleMockHandler struct {
	startCtx        atomic.Value
	forceExitCount  atomic.Int32
	shutdownCount   atomic.Int32
	forceExitSignal chan struct{}
}

func (m *scheduleMockHandler) Start(ctx context.Context) error {
	m.startCtx.Store(ctx)
	return nil
}

func (m *scheduleMockHandler) ForceExit(ctx context.Context) error {
	m.forceExitCount.Add(1)
	if m.forceExitSignal != nil {
		select {
		case m.forceExitSignal <- struct{}{}:
		default:
		}
	}
	return nil
}

func (m *scheduleMockHandler) Shutdown(ctx context.Context) error {
	m.shutdownCount.Add(1)
	return nil
}

func (m *scheduleMockHandler) PrintReport(enableCSV bool) {}

func TestEngineRun_TwoPhaseForceExitThenKillSwitch(t *testing.T) {
	t.Setenv("BOT_FORCE_EXIT_TIME", "15:23")
	t.Setenv("BOT_EXIT_TIME", "15:32")

	jst := time.FixedZone("JST", 9*3600)
	var timeMu sync.Mutex
	currentTime := time.Date(2026, 11, 25, 15, 22, 0, 0, jst)

	handler := &scheduleMockHandler{
		forceExitSignal: make(chan struct{}, 1),
	}
	eng := NewEngine(handler)
	eng.checkInterval = 5 * time.Millisecond
	eng.nowFunc = func() time.Time {
		timeMu.Lock()
		defer timeMu.Unlock()
		return currentTime
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- eng.Run(ctx)
	}()

	// 15:22 の間は ForceExit も Shutdown も発火しない
	time.Sleep(30 * time.Millisecond)
	if handler.forceExitCount.Load() != 0 {
		t.Fatalf("expected 0 ForceExit calls at 15:22, got %d", handler.forceExitCount.Load())
	}
	if handler.shutdownCount.Load() != 0 {
		t.Fatalf("expected 0 Shutdown calls at 15:22, got %d", handler.shutdownCount.Load())
	}

	// 15:23 に進める -> ForceExit のみ1回発火し、バックグラウンドコンテキストは継続する
	timeMu.Lock()
	currentTime = time.Date(2026, 11, 25, 15, 23, 0, 0, jst)
	timeMu.Unlock()

	select {
	case <-handler.forceExitSignal:
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for ForceExit at 15:23")
	}

	// 15:25 に進めても ForceExit は再発火せず、Shutdown もまだ呼ばれない（Tickデータ収集継続中）
	timeMu.Lock()
	currentTime = time.Date(2026, 11, 25, 15, 25, 0, 0, jst)
	timeMu.Unlock()
	time.Sleep(30 * time.Millisecond)

	if handler.forceExitCount.Load() != 1 {
		t.Fatalf("expected exactly 1 ForceExit call before 15:32, got %d", handler.forceExitCount.Load())
	}
	if handler.shutdownCount.Load() != 0 {
		t.Fatalf("expected 0 Shutdown calls before 15:32, got %d", handler.shutdownCount.Load())
	}
	bgCtx := handler.startCtx.Load().(context.Context)
	if bgCtx.Err() != nil {
		t.Fatalf("expected background context to remain active for tick recording until 15:32, got %v", bgCtx.Err())
	}

	// 15:32 に進める -> キルスイッチ作動して Shutdown 後に正常終了
	timeMu.Lock()
	currentTime = time.Date(2026, 11, 25, 15, 32, 0, 0, jst)
	timeMu.Unlock()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected Run to return nil, got %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for Run to finish at 15:32")
	}

	if handler.shutdownCount.Load() != 1 {
		t.Fatalf("expected 1 Shutdown call at 15:32, got %d", handler.shutdownCount.Load())
	}
	if bgCtx.Err() == nil {
		t.Fatal("expected background context to be canceled after Shutdown")
	}
}
