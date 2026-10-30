package logger

import (
	"bufio"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

var (
	// ErrClosed はAsyncWriterが既にクローズされている場合に返されるエラーです。
	ErrClosed = errors.New("async writer is closed")
)

// AsyncWriter はバックグラウンドgoroutineでバッファリング書き込みを行う非同期io.WriteCloserです。
// メインの処理ループをディスクI/Oから完全に分離し、超低遅延でのロギングを実現します。
type AsyncWriter struct {
	underlying io.Writer
	queue      chan []byte
	done       chan struct{}
	closed     atomic.Bool
	closeOnce  sync.Once
	flushReq   chan chan struct{}
}

// NewAsyncWriter は指定されたバッファサイズで非同期Writerを生成します。
// bufferSize: キューの最大行数（推奨: 4096〜16384）
func NewAsyncWriter(w io.Writer, bufferSize int) *AsyncWriter {
	if bufferSize <= 0 {
		bufferSize = 4096
	}

	aw := &AsyncWriter{
		underlying: w,
		queue:      make(chan []byte, bufferSize),
		done:       make(chan struct{}),
		flushReq:   make(chan chan struct{}),
	}

	go aw.worker()

	return aw
}

// Write はログバイト列をキューに送信して即時リターンします。
// 呼び出し元のスライス再利用に備えて内部でメモリコピーを行います。
func (aw *AsyncWriter) Write(p []byte) (n int, err error) {
	if aw.closed.Load() {
		return 0, ErrClosed
	}

	// 呼び出し元のバッファ再利用によるデータ競合を防ぐためコピー
	data := make([]byte, len(p))
	copy(data, p)

	aw.queue <- data
	return len(p), nil
}

// worker はバックグラウンドでチャネルを監視し、bufio経由で一括書き込みおよび定期フラッシュを行います。
func (aw *AsyncWriter) worker() {
	defer close(aw.done)

	// 64KBのメモリバッファリングでシステムコール回数を激減させる
	bw := bufio.NewWriterSize(aw.underlying, 64*1024)

	// 一定時間書き込みが途切れた場合でも最大200ms以内にディスクへフラッシュ
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case data, ok := <-aw.queue:
			if !ok {
				// キューがクローズされた場合は残りを全て吐き出して終了
				_ = bw.Flush()
				if closer, ok := aw.underlying.(io.Closer); ok {
					_ = closer.Close()
				}
				return
			}
			_, _ = bw.Write(data)

			// キュー内にまだデータがあれば一気に書き出す（ドレイン処理）
			draining := true
			for draining {
				select {
				case nextData, ok := <-aw.queue:
					if !ok {
						_ = bw.Flush()
						if closer, ok := aw.underlying.(io.Closer); ok {
							_ = closer.Close()
						}
						return
					}
					_, _ = bw.Write(nextData)
				default:
					draining = false
				}
			}

		case <-ticker.C:
			_ = bw.Flush()

		case resp := <-aw.flushReq:
			// キューに残っているデータを全てbwに書き出してからFlushする
			draining := true
			for draining {
				select {
				case nextData, ok := <-aw.queue:
					if !ok {
						_ = bw.Flush()
						if closer, ok := aw.underlying.(io.Closer); ok {
							_ = closer.Close()
						}
						close(resp)
						return
					}
					_, _ = bw.Write(nextData)
				default:
					draining = false
				}
			}
			_ = bw.Flush()
			close(resp)
		}
	}
}

// Flush は現在キューに入っているすべてのデータを下流Writerに同期フラッシュします。
func (aw *AsyncWriter) Flush() error {
	if aw.closed.Load() {
		return ErrClosed
	}

	resp := make(chan struct{})
	aw.flushReq <- resp
	<-resp
	return nil
}

// Close はキューを閉じ、残存するすべてのログデータをディスクへフラッシュした後に終了します。
// 下流のWriterがio.Closerを実装している場合はそのClose()も呼び出します。
func (aw *AsyncWriter) Close() error {
	var err error
	aw.closeOnce.Do(func() {
		aw.closed.Store(true)
		close(aw.queue)
		<-aw.done
	})
	return err
}
