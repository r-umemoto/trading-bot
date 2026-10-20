package kabu

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
)

type OrderJob struct {
	Key         string // deduplication key (JobID)
	Symbol      string
	OrderID     string // キャンセルの場合に使用
	OrderPtr    *order.Order
	Priority    int // アクション・戦略に基づく優先度
	UpdateCount int
	RequestedAt time.Time
	ResultChan  chan order.OrderResult // 追加: 結果を返すためのチャネル
}

// Sender は Dispatcher が実際に発注・キャンセルを実行するためのインターフェースです
type Sender interface {
	SendOrderRaw(ctx context.Context, input order.SendOrderInput) (*order.Order, error)
	CancelOrderRaw(ctx context.Context, orderID string) error
}

// OrderDispatcher は秒間発注制限を維持しながら優先度順に発注を配信するインフラ層の門番です
type OrderDispatcher struct {
	sender        Sender
	pendingJobs   []*OrderJob
	inFlightKeys  map[string]time.Time
	completedKeys map[string]time.Time
	jobMu         sync.Mutex
}

func NewOrderDispatcher(sender Sender) *OrderDispatcher {
	return &OrderDispatcher{
		sender:        sender,
		pendingJobs:   make([]*OrderJob, 0),
		inFlightKeys:  make(map[string]time.Time),
		completedKeys: make(map[string]time.Time),
	}
}

func (d *OrderDispatcher) Start(ctx context.Context) {
	go d.dispatchWorker(ctx)
}

func (d *OrderDispatcher) Submit(jobID string, symbol string, ord *order.Order, cancelID string, priority int) <-chan order.OrderResult {
	resCh := make(chan order.OrderResult, 1)

	d.jobMu.Lock()
	defer d.jobMu.Unlock()

	now := time.Now()

	// 1. 同一キーが現在 API 送信中 (In-Flight) の場合は二重送信を厳格に遮断
	if jobID != "" {
		if inFlightSince, exists := d.inFlightKeys[jobID]; exists {
			// 10秒以上スタックしていなければ In-Flight 中として重複排除
			if now.Sub(inFlightSince) < 10*time.Second {
				resCh <- order.OrderResult{
					Symbol: symbol,
					Order:  ord,
					Error:  fmt.Errorf("%w: identical order/job is already in-flight (%s)", order.ErrOrderSkipped, jobID),
				}
				close(resCh)
				return resCh
			}
		}

		// 2. 直近 (1秒以内) に同一キーが送信完了したばかりの場合の連射（リバウンド）を防止
		if completedAt, exists := d.completedKeys[jobID]; exists {
			if now.Sub(completedAt) < 1*time.Second {
				resCh <- order.OrderResult{
					Symbol: symbol,
					Order:  ord,
					Error:  fmt.Errorf("%w: identical order/job completed recently (%s)", order.ErrOrderSkipped, jobID),
				}
				close(resCh)
				return resCh
			}
		}
	}

	requestedAt := now
	updateCount := 0

	// 同じ jobID の既存ジョブを探して上書き（古い方をエラーでクローズして削除）
	for i, old := range d.pendingJobs {
		if old.Key == jobID {
			requestedAt = old.RequestedAt
			updateCount = old.UpdateCount + 1

			old.ResultChan <- order.OrderResult{
				Symbol: old.Symbol,
				Order:  old.OrderPtr,
				Error:  fmt.Errorf("%w: order overwritten in dispatch queue", order.ErrDispatchQueueBypass),
			}
			close(old.ResultChan)
			d.pendingJobs = append(d.pendingJobs[:i], d.pendingJobs[i+1:]...)
			break
		}
	}

	if ord == nil && cancelID == "" {
		close(resCh)
		return resCh
	}

	d.pendingJobs = append(d.pendingJobs, &OrderJob{
		Key:         jobID,
		Symbol:      symbol,
		OrderID:     cancelID,
		OrderPtr:    ord,
		Priority:    priority,
		UpdateCount: updateCount,
		RequestedAt: requestedAt,
		ResultChan:  resCh,
	})
	return resCh
}

func (d *OrderDispatcher) dispatchWorker(ctx context.Context) {
	ticker := time.NewTicker(100 * time.Millisecond) // 秒間10発注制限
	defer ticker.Stop()

	cleanupTicker := time.NewTicker(10 * time.Second)
	defer cleanupTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-cleanupTicker.C:
			d.cleanupStaleKeys(now)
		case <-ticker.C:
			job := d.pickBestJob()
			if job != nil {
				go d.executeJob(ctx, job)
			}
		}
	}
}

func (d *OrderDispatcher) cleanupStaleKeys(now time.Time) {
	d.jobMu.Lock()
	defer d.jobMu.Unlock()
	for k, t := range d.completedKeys {
		if now.Sub(t) > 30*time.Second {
			delete(d.completedKeys, k)
		}
	}
	for k, t := range d.inFlightKeys {
		if now.Sub(t) > 30*time.Second {
			delete(d.inFlightKeys, k)
		}
	}
}

func (d *OrderDispatcher) pickBestJob() *OrderJob {
	d.jobMu.Lock()
	defer d.jobMu.Unlock()

	if len(d.pendingJobs) == 0 {
		return nil
	}

	bestIdx := 0
	for i := 1; i < len(d.pendingJobs); i++ {
		scoreI := d.pendingJobs[i].Priority + d.pendingJobs[i].UpdateCount
		scoreBest := d.pendingJobs[bestIdx].Priority + d.pendingJobs[bestIdx].UpdateCount
		if scoreI > scoreBest {
			bestIdx = i
		} else if scoreI == scoreBest {
			if d.pendingJobs[i].RequestedAt.Before(d.pendingJobs[bestIdx].RequestedAt) {
				bestIdx = i
			}
		}
	}

	best := d.pendingJobs[bestIdx]
	d.pendingJobs = append(d.pendingJobs[:bestIdx], d.pendingJobs[bestIdx+1:]...)

	if best.Key != "" {
		d.inFlightKeys[best.Key] = time.Now()
	}

	return best
}

func (d *OrderDispatcher) executeJob(ctx context.Context, job *OrderJob) {
	defer close(job.ResultChan)
	defer func() {
		if job.Key != "" {
			d.jobMu.Lock()
			delete(d.inFlightKeys, job.Key)
			d.completedKeys[job.Key] = time.Now()
			d.jobMu.Unlock()
		}
	}()

	apiCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if job.OrderID != "" {
		err := d.sender.CancelOrderRaw(apiCtx, job.OrderID)
		job.ResultChan <- order.OrderResult{
			Symbol:  job.Symbol,
			OrderID: job.OrderID,
			Error:   err,
		}
		return
	}

	if job.OrderPtr != nil {
		updatedOrder, err := d.sender.SendOrderRaw(apiCtx, order.SendOrderInput{Order: job.OrderPtr})
		if err != nil {
			job.ResultChan <- order.OrderResult{
				Symbol: job.Symbol,
				Order:  job.OrderPtr,
				Error:  err,
			}
			return
		}
		// 指針: 内部の状態を更新
		*job.OrderPtr = *updatedOrder
		job.ResultChan <- order.OrderResult{
			Symbol:  job.Symbol,
			OrderID: updatedOrder.ID,
			Order:   job.OrderPtr,
		}
	}
}

// CancelPendingJob は指定された orderID を持つ送信前ジョブをキューから見つけて削除し、
// そのジョブの ResultChan に上書きキャンセルエラーを送ってクローズします。
// 削除に成功した場合は true を返します。
func (d *OrderDispatcher) CancelPendingJob(orderID string) bool {
	d.jobMu.Lock()
	defer d.jobMu.Unlock()

	for i, job := range d.pendingJobs {
		if job.OrderPtr != nil && job.OrderPtr.ID == orderID {
			job.ResultChan <- order.OrderResult{
				Symbol: job.Symbol,
				Order:  job.OrderPtr,
				Error:  fmt.Errorf("%w: order canceled in dispatch queue", order.ErrDispatchQueueBypass),
			}
			close(job.ResultChan)
			d.pendingJobs = append(d.pendingJobs[:i], d.pendingJobs[i+1:]...)
			return true
		}
	}
	return false
}
