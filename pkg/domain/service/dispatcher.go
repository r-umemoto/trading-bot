package service

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
)

type OrderJob struct {
	Symbol      string
	Sniper      *sniper.Sniper
	CancelID    string
	OrderPtr    *order.Order
	OrderReq    *order.OrderRequest
	Priority    int
	UpdateCount int // 🌟 上書き（待機）回数。これが多いほど優先度が上がる
	RequestedAt time.Time
}

// OrderDispatcher は発注要求を受け取り、秒間発注制限を維持しながら優先度順に配信する門番です
type OrderDispatcher struct {
	gateway     market.MarketGateway
	pendingJobs map[string]*OrderJob
	jobMu       sync.Mutex
}

func NewOrderDispatcher(gateway market.MarketGateway) *OrderDispatcher {
	return &OrderDispatcher{
		gateway:     gateway,
		pendingJobs: make(map[string]*OrderJob),
	}
}

// Start は発注ディスパッチャ（秒間10回制限の門番）を起動します
func (d *OrderDispatcher) Start(ctx context.Context) {
	go d.dispatchWorker(ctx)
}

// Submit は新しい発注・キャンセル要求をキューに登録（または既存分を更新）します
func (d *OrderDispatcher) Submit(s *sniper.Sniper, bullet sniper.Bullet) {
	if !bullet.HasOrder() && !bullet.HasCancel() {
		// 意図が HOLD の場合、もし待機中のジョブがあれば削除する（最新の意図を優先）
		d.jobMu.Lock()
		delete(d.pendingJobs, s.Detail.Code)
		d.jobMu.Unlock()
		return
	}

	priority := 1 // デフォルトは低優先（新規エントリー：ENTRY）
	if bullet.HasCancel() {
		priority = 10 // キャンセルは中優先（CANCEL）
	} else if bullet.Request != nil && (bullet.Request.ClosePositionOrder != order.CLOSE_POSITION_ORDER_NONE || len(bullet.Request.ClosePositions) > 0) {
		priority = 20 // 決済注文（損切り・利確：EXIT）は最優先
	}

	d.jobMu.Lock()
	existing, ok := d.pendingJobs[s.Detail.Code]
	updateCount := 0
	if ok {
		// すでに待機中のジョブがあれば、その更新回数（溜まっていたフラストレーション）を引き継ぐ
		updateCount = existing.UpdateCount + 1
	}

	job := &OrderJob{
		Symbol:      s.Detail.Code,
		Sniper:      s,
		CancelID:    bullet.CancelOrderID,
		OrderPtr:    bullet.Order,
		OrderReq:    bullet.Request,
		Priority:    priority,
		UpdateCount: updateCount,
		RequestedAt: time.Now(),
	}

	d.pendingJobs[s.Detail.Code] = job
	d.jobMu.Unlock()
}

func (d *OrderDispatcher) dispatchWorker(ctx context.Context) {
	ticker := time.NewTicker(110 * time.Millisecond) // 約9回/秒
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

	// 全ジョブをリスト化してソート
	var jobs []*OrderJob
	for _, j := range d.pendingJobs {
		jobs = append(jobs, j)
	}

	sort.Slice(jobs, func(i, j int) bool {
		// 🌟 優先度 + 更新回数（お待たせボーナス）で総合スコアを計算
		scoreI := jobs[i].Priority + jobs[i].UpdateCount
		scoreJ := jobs[j].Priority + jobs[j].UpdateCount

		if scoreI != scoreJ {
			return scoreI > scoreJ
		}
		// 2. 同じスコアなら、より古いリクエスト順
		return jobs[i].RequestedAt.Before(jobs[j].RequestedAt)
	})

	best := jobs[0]
	delete(d.pendingJobs, best.Symbol)
	return best
}

func (d *OrderDispatcher) executeJob(ctx context.Context, job *OrderJob) {
	// API通信用のタイムアウト付きコンテキスト
	apiCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if job.CancelID != "" {
		fmt.Printf("🛑 [%s] 注文キャンセルを実行中 (ID: %s, Priority: %d)\n", job.Symbol, job.CancelID, job.Priority)
		if err := d.gateway.CancelOrder(apiCtx, job.CancelID); err != nil {
			fmt.Printf("❌ [%s] キャンセル失敗: %v\n", job.Symbol, err)
		}
		return
	}

	if job.OrderPtr != nil {
		fmt.Printf("🚀 [%s] 発注を実行中 (%s %.0f株 @%.1f, Priority: %d)\n", job.Symbol, job.OrderPtr.Action, job.OrderPtr.OrderQty, job.OrderPtr.OrderPrice, job.Priority)
		updatedOrder, err := d.gateway.SendOrder(apiCtx, order.SendOrderInput{Order: *job.OrderPtr, Request: *job.OrderReq})
		if err != nil {
			fmt.Printf("❌ [%s] 発注失敗: %v\n", job.Symbol, err)
			job.Sniper.FailSendingOrder(job.OrderPtr)
			return
		}

		// 成功したらGatewayから返された updatedOrder でポインタの中身を上書きする
		*job.OrderPtr = updatedOrder
		fmt.Printf("✅ [%s] 注文受付完了 (ID: %s)\n", job.Symbol, job.OrderPtr.ID)
	}
}
