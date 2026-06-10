package engine_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/engine"
)

type mockUseCaseHandler struct {
	startFunc       func(ctx context.Context) error
	shutdownFunc    func(ctx context.Context) error
	printReportFunc func(enableCSV bool)
}

func (m *mockUseCaseHandler) Start(ctx context.Context) error {
	if m.startFunc != nil {
		return m.startFunc(ctx)
	}
	return nil
}

func (m *mockUseCaseHandler) Shutdown(ctx context.Context) error {
	if m.shutdownFunc != nil {
		return m.shutdownFunc(ctx)
	}
	return nil
}

func (m *mockUseCaseHandler) PrintReport(enableCSV bool) {
	if m.printReportFunc != nil {
		m.printReportFunc(enableCSV)
	}
}

func TestNewEngine(t *testing.T) {
	mockUC := &mockUseCaseHandler{}
	eng := engine.NewEngine(mockUC)
	if eng == nil {
		t.Fatal("expected NewEngine to return a non-nil Engine")
	}
}

func TestPrintReport(t *testing.T) {
	called := false
	var passedEnableCSV bool
	mockUC := &mockUseCaseHandler{
		printReportFunc: func(enableCSV bool) {
			called = true
			passedEnableCSV = enableCSV
		},
	}
	eng := engine.NewEngine(mockUC)
	eng.PrintReport(true)

	if !called {
		t.Error("expected PrintReport to be called on usecase")
	}
	if !passedEnableCSV {
		t.Error("expected enableCSV=true to be passed to usecase.PrintReport")
	}
}

func TestEngineRun_Success(t *testing.T) {
	var mu sync.Mutex
	startCalled := false
	shutdownCalled := false

	mockUC := &mockUseCaseHandler{
		startFunc: func(ctx context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			startCalled = true
			return nil
		},
		shutdownFunc: func(ctx context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			shutdownCalled = true
			return nil
		},
	}

	eng := engine.NewEngine(mockUC)
	ctx, cancel := context.WithCancel(context.Background())

	// Run inside a goroutine since it will block until ctx is done or kill switch triggers
	errChan := make(chan error, 1)
	go func() {
		errChan <- eng.Run(ctx)
	}()

	// Wait a moment to ensure Start is called
	time.Sleep(50 * time.Millisecond)

	// Cancel the context to trigger the shutdown
	cancel()

	// Wait for Run to return
	var err error
	select {
	case err = <-errChan:
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for Run to return")
	}

	if err != nil {
		t.Errorf("expected Run to return nil, got: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !startCalled {
		t.Error("expected usecase.Start to be called")
	}
	if !shutdownCalled {
		t.Error("expected usecase.Shutdown to be called")
	}
}

func TestEngineRun_StartError(t *testing.T) {
	startErr := errors.New("start failed")
	mockUC := &mockUseCaseHandler{
		startFunc: func(ctx context.Context) error {
			return startErr
		},
		shutdownFunc: func(ctx context.Context) error {
			t.Error("usecase.Shutdown should not be called when Start fails")
			return nil
		},
	}

	eng := engine.NewEngine(mockUC)
	ctx := context.Background()

	err := eng.Run(ctx)
	if !errors.Is(err, startErr) {
		t.Errorf("expected error %v, got %v", startErr, err)
	}
}

func TestEngineRun_ShutdownError(t *testing.T) {
	shutdownErr := errors.New("shutdown failed")
	mockUC := &mockUseCaseHandler{
		startFunc: func(ctx context.Context) error {
			return nil
		},
		shutdownFunc: func(ctx context.Context) error {
			return shutdownErr
		},
	}

	eng := engine.NewEngine(mockUC)
	ctx, cancel := context.WithCancel(context.Background())

	errChan := make(chan error, 1)
	go func() {
		errChan <- eng.Run(ctx)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	var err error
	select {
	case err = <-errChan:
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for Run to return")
	}

	if !errors.Is(err, shutdownErr) {
		t.Errorf("expected error %v, got %v", shutdownErr, err)
	}
}
