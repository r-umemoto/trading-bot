package kabu

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
)

type OrderJob struct {
	Symbol      string
	OrderID     string // キャンセルの場合に使用
	OrderPtr    *order.Order
	OrderReq    *order.OrderRequest
	Priority    int
	UpdateCount int
	RequestedAt time.Time
	ResultChan  chan order.OrderResult // 追加: 結果を返すためのチャネル
}

// Sender は Dispatcher が実際に発注・キャンセルを実行するためのインターフェースです
type Sender interface {
	SendOrderRaw(ctx context.Context, input order.SendOrderInput) (order.Order, error)
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

func (d *OrderDispatcher) Submit(symbol string, ord *order.Order, req *order.OrderRequest, cancelID string) <-chan order.OrderResult {
	resCh := make(chan order.OrderResult, 1)
	if ord == nil && cancelID == "" {
		d.jobMu.Lock()
		delete(d.pendingJobs, symbol)
		d.jobMu.Unlock()
		close(resCh)
		return resCh
	}

	priority := 1
	if cancelID != "" {
		priority = 10
	} else if req != nil && (req.ClosePositionOrder != order.CLOSE_POSITION_ORDER_NONE || len(req.ClosePositions) > 0) {
		priority = 20
	}

	d.jobMu.Lock()
	d.pendingJobs[symbol] = &OrderJob{
		Symbol:      symbol,
		OrderID:     cancelID,
		OrderPtr:    ord,
		OrderReq:    req,
		Priority:    priority,
		RequestedAt: time.Now(),
		ResultChan:  resCh,
	}
	d.jobMu.Unlock()
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
		updatedOrder, err := d.sender.SendOrderRaw(apiCtx, order.SendOrderInput{Order: *job.OrderPtr, Request: *job.OrderReq})
		if err != nil {
			job.ResultChan <- order.OrderResult{
				Symbol: job.Symbol,
				Order:  job.OrderPtr,
				Error:  err,
			}
			return
		}
		*job.OrderPtr = updatedOrder
		job.ResultChan <- order.OrderResult{
			Symbol:  job.Symbol,
			OrderID: updatedOrder.ID,
			Order:   job.OrderPtr,
		}
	}
}
