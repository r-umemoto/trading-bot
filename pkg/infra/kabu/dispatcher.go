package kabu

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
)

type OrderJob struct {
	Symbol      string
	OrderID     string // キャンセルの場合に使用
	OrderPtr    *order.Order
	Priority    int
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
	sender      Sender
	pendingJobs map[string]*OrderJob
	jobMu       sync.Mutex
}

func NewOrderDispatcher(sender Sender) *OrderDispatcher {
	return &OrderDispatcher{
		sender:      sender,
		pendingJobs: make(map[string]*OrderJob),
	}
}

func (d *OrderDispatcher) Start(ctx context.Context) {
	go d.dispatchWorker(ctx)
}

func (d *OrderDispatcher) Submit(symbol string, ord *order.Order, cancelID string) <-chan order.OrderResult {
	resCh := make(chan order.OrderResult, 1)

	d.jobMu.Lock()
	defer d.jobMu.Unlock()

	requestedAt := time.Now()
	updateCount := 0

	// 既存の同一 symbol に対するジョブがある場合は上書きキャンセルする（ResultChanリークを防ぐ）
	if old, exists := d.pendingJobs[symbol]; exists {
		// 古いジョブの RequestedAt と UpdateCount を引き継ぐ（優先度が下がるのを防ぐ）
		requestedAt = old.RequestedAt
		updateCount = old.UpdateCount + 1

		old.ResultChan <- order.OrderResult{
			Symbol: old.Symbol,
			Order:  old.OrderPtr,
			Error:  fmt.Errorf("order overwritten in dispatch queue"),
		}
		close(old.ResultChan)
		delete(d.pendingJobs, symbol)
	}

	if ord == nil && cancelID == "" {
		close(resCh)
		return resCh
	}

	priority := 1
	if cancelID != "" {
		priority = 10
	} else if ord != nil && ord.Request != nil && (ord.Request.ClosePositionOrder != order.CLOSE_POSITION_ORDER_NONE || len(ord.Request.ClosePositions) > 0) {
		priority = 20
	}

	// 既にロックを取得しているので直接登録する
	d.pendingJobs[symbol] = &OrderJob{
		Symbol:      symbol,
		OrderID:     cancelID,
		OrderPtr:    ord,
		Priority:    priority,
		UpdateCount: updateCount,
		RequestedAt: requestedAt,
		ResultChan:  resCh,
	}
	return resCh
}

func (d *OrderDispatcher) dispatchWorker(ctx context.Context) {
	ticker := time.NewTicker(100 * time.Millisecond) // 秒間10発注制限
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			job := d.pickBestJob()
			if job != nil {
				go d.executeJob(ctx, job)
			}
		}
	}
}

func (d *OrderDispatcher) pickBestJob() *OrderJob {
	d.jobMu.Lock()
	defer d.jobMu.Unlock()

	if len(d.pendingJobs) == 0 {
		return nil
	}

	var jobs []*OrderJob
	for _, j := range d.pendingJobs {
		jobs = append(jobs, j)
	}

	sort.Slice(jobs, func(i, j int) bool {
		scoreI := jobs[i].Priority + jobs[i].UpdateCount
		scoreJ := jobs[j].Priority + jobs[j].UpdateCount
		if scoreI != scoreJ {
			return scoreI > scoreJ
		}
		return jobs[i].RequestedAt.Before(jobs[j].RequestedAt)
	})

	best := jobs[0]
	delete(d.pendingJobs, best.Symbol)
	return best
}

func (d *OrderDispatcher) executeJob(ctx context.Context, job *OrderJob) {
	defer close(job.ResultChan)
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

	for symbol, job := range d.pendingJobs {
		if job.OrderPtr != nil && job.OrderPtr.ID == orderID {
			job.ResultChan <- order.OrderResult{
				Symbol: job.Symbol,
				Order:  job.OrderPtr,
				Error:  fmt.Errorf("order canceled in dispatch queue"),
			}
			close(job.ResultChan)
			delete(d.pendingJobs, symbol)
			return true
		}
	}
	return false
}
