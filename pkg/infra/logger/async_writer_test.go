package logger_test

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/infra/logger"
)

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func TestAsyncWriter_Basic(t *testing.T) {
	buf := &safeBuffer{}
	aw := logger.NewAsyncWriter(buf, 100)

	msg1 := []byte("hello line 1\n")
	msg2 := []byte("hello line 2\n")

	n, err := aw.Write(msg1)
	if err != nil || n != len(msg1) {
		t.Fatalf("Write failed: n=%d, err=%v", n, err)
	}

	n, err = aw.Write(msg2)
	if err != nil || n != len(msg2) {
		t.Fatalf("Write failed: n=%d, err=%v", n, err)
	}

	// Close calls flush
	if err := aw.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	expected := "hello line 1\nhello line 2\n"
	if buf.String() != expected {
		t.Errorf("expected %q, got %q", expected, buf.String())
	}

	// Writing after close should fail
	_, err = aw.Write([]byte("after close"))
	if err != logger.ErrClosed {
		t.Errorf("expected ErrClosed, got %v", err)
	}
}

func TestAsyncWriter_Flush(t *testing.T) {
	buf := &safeBuffer{}
	aw := logger.NewAsyncWriter(buf, 100)
	defer aw.Close()

	msg := []byte("flush test\n")
	_, err := aw.Write(msg)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	if err := aw.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	if buf.String() != string(msg) {
		t.Errorf("expected %q, got %q", string(msg), buf.String())
	}
}

func TestAsyncWriter_ConcurrentLoad(t *testing.T) {
	buf := &safeBuffer{}
	aw := logger.NewAsyncWriter(buf, 10000)

	const goroutines = 20
	const msgsPerGoroutine = 500

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < msgsPerGoroutine; i++ {
				msg := []byte(fmt.Sprintf("[g%d] message %d\n", gid, i))
				_, _ = aw.Write(msg)
			}
		}(g)
	}

	wg.Wait()

	if err := aw.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Verify all messages were received
	lines := bytes.Count([]byte(buf.String()), []byte("\n"))
	expectedLines := goroutines * msgsPerGoroutine
	if lines != expectedLines {
		t.Errorf("expected %d lines, got %d", expectedLines, lines)
	}
}

func TestAsyncWriter_TickerFlush(t *testing.T) {
	buf := &safeBuffer{}
	aw := logger.NewAsyncWriter(buf, 100)
	defer aw.Close()

	msg := []byte("ticker test\n")
	_, _ = aw.Write(msg)

	// Wait for ticker (200ms + buffer)
	time.Sleep(300 * time.Millisecond)

	if buf.String() != string(msg) {
		t.Errorf("expected ticker to flush %q, got %q", string(msg), buf.String())
	}
}
