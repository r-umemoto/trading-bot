// internal/usecase/trade_usecase.go
package usecase

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/ord"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

type OrderJob struct {
	Symbol      string
	Sniper      *sniper.Sniper
	CancelID    string
	OrderPtr    *ord.Order
	Priority    int
	UpdateCount int // 🌟 上書き（待機）回数。これが多いほど優先度が上がる
	RequestedAt time.Time
}

// TradeUseCase は価格更新イベントを受け取り、該当するスナイパーに伝達するユースケースです
type TradeUseCase struct {
	snipers       []*sniper.Sniper
	gateway       market.MarketGateway
	dataPool      tick.DataPool
	tickChannels  map[string]chan tick.Tick  // 銘柄ごとのTick処理チャネル
	orderChannels map[string]chan ord.Orders // 銘柄ごとの注文レポートチャネル

	// 🌟 発注ディスパッチャ用の状態
	pendingJobs map[string]*OrderJob
	jobMu       sync.Mutex
}

func NewTradeUseCase(snipers []*sniper.Sniper, gateway market.MarketGateway, dataPool tick.DataPool) *TradeUseCase {
	uc := &TradeUseCase{
		snipers:       snipers,
		gateway:       gateway,
		dataPool:      dataPool,
		tickChannels:  make(map[string]chan tick.Tick),
		orderChannels: make(map[string]chan ord.Orders),
		pendingJobs:   make(map[string]*OrderJob),
	}

	// 銘柄ごとにチャネルを作成
	for _, s := range snipers {
		if _, exists := uc.tickChannels[s.Detail.Code]; !exists {
			// バッファサイズを拡張 (100 -> 1000) してスパイク耐性を高める
			uc.tickChannels[s.Detail.Code] = make(chan tick.Tick, 1000)
			uc.orderChannels[s.Detail.Code] = make(chan ord.Orders, 1000)
		}
	}

	return uc
}

// StartWorkers は銘柄ごとのワーカー（Goroutine）を起動します
// Engineの起動時（Run）などに呼ばれることを想定しています
func (u *TradeUseCase) StartWorkers(ctx context.Context) {
	// 1. 各銘柄のロジックワーカーを起動
	for symbol := range u.tickChannels {
		go u.worker(ctx, symbol, u.tickChannels[symbol], u.orderChannels[symbol])
	}

	// 2. 🌟 全銘柄共通の発注ディスパッチャ（秒間10回制限の門番）を起動
	go u.dispatchWorker(ctx)
}

// worker は特定の銘柄のTickや約定通知を専用に処理するGoroutineです
func (u *TradeUseCase) worker(ctx context.Context, symbol string, tickCh <-chan tick.Tick, orderCh <-chan ord.Orders) {
	for {
		select {
		case <-ctx.Done():
			return
		case tick := <-tickCh:
			// この銘柄を担当するスナイパーを探してTick処理を実行
			u.processTickForSymbol(ctx, tick, symbol)
		case report := <-orderCh:
			// この銘柄を担当するスナイパーを探して注文同期を実行
			u.processOrdersReportForSymbol(ctx, report, symbol)
		}
	}
}

func (u *TradeUseCase) processTickForSymbol(ctx context.Context, tick tick.Tick, symbol string) {
	u.dataPool.PushTick(tick)

	for _, s := range u.snipers {
		if s.Detail.Code == symbol {
			// 1. スナイパーに考えさせる
			orderPtr, cancelOrderID := s.Tick(u.dataPool)

			// 2. 🌟 直接発注せず、ディスパッチャに委ねる
			u.submitJob(s, cancelOrderID, orderPtr)
		}
	}
}

// HandleTick は市場のTickデータを受け取り、該当銘柄のチャネルへルーティングします
func (u *TradeUseCase) HandleTick(ctx context.Context, tick tick.Tick) {
	if ch, ok := u.tickChannels[tick.Symbol]; ok {
		select {
		case ch <- tick:
			// 正常にキューイング完了
		default:
			// 🚨 チャネルがフル（ワーカーが重い、またはAPI遅延中）の場合は、
			// 上流のWebSocket受信を止めないために「最新のTick以外は破棄（スキップ）」する。
			fmt.Printf("⚠️ [%s] ワーカー過負荷: Tickチャネルがフルのため、このTickを破棄(スキップ)します\n", tick.Symbol)
		}
	}
}

// HandleExecution は、インフラ層から流れてきた注文レポートを該当銘柄のチャネルへルーティングします
func (u *TradeUseCase) HandleExecution(report ord.Orders) {
	// 全銘柄分を個別にルーティングするのは効率が悪いが、現状の構造に合わせる
	// 本来は OrdersReport が銘柄ごとに分割されているか、ここで分割して投げる
	for symbol, ch := range u.orderChannels {
		select {
		case ch <- report:
		default:
			// 🚨 約定通知は絶対に破棄してはいけない（状態がズレるため）。
			// ただし、メインスレッドをブロックすると全銘柄に波及するため、非同期で押し込む。
			fmt.Printf("⚠️ 🚨 [%s] OrdersReportチャネルがフルです。非同期でキューイングを試みます。\n", symbol)
			go func(c chan<- ord.Orders, r ord.Orders) {
				c <- r // バックグラウンドでブロックして待つ
			}(ch, report)
		}
	}
}

func (u *TradeUseCase) processOrdersReportForSymbol(ctx context.Context, report ord.Orders, symbol string) {
	for _, s := range u.snipers {
		if s.Detail.Code == symbol {
			orderPtr, cancelOrderID := s.SyncOrders(report)

			// 2. 🌟 直接発注せず、ディスパッチャに委ねる
			u.submitJob(s, cancelOrderID, orderPtr)
		}
	}
}

// submitJob は新しい発注・キャンセル要求をキューに登録（または既存分を更新）します
func (u *TradeUseCase) submitJob(s *sniper.Sniper, cancelID string, orderPtr *ord.Order) {
	if orderPtr == nil && cancelID == "" {
		// 意図が HOLD の場合、もし待機中のジョブがあれば削除する（最新の意図を優先）
		u.jobMu.Lock()
		delete(u.pendingJobs, s.Detail.Code)
		u.jobMu.Unlock()
		return
	}

	priority := 1 // デフォルトは低優先（新規エントリー：ENTRY）
	if cancelID != "" {
		priority = 10 // キャンセルは中優先（CANCEL）
	} else if orderPtr != nil && (orderPtr.ClosePositionOrder != ord.CLOSE_POSITION_ORDER_NONE || len(orderPtr.ClosePositions) > 0) {
		priority = 20 // 決済注文（損切り・利確：EXIT）は最優先
	}

	u.jobMu.Lock()
	existing, ok := u.pendingJobs[s.Detail.Code]
	updateCount := 0
	if ok {
		// すでに待機中のジョブがあれば、その更新回数（溜まっていたフラストレーション）を引き継ぐ
		updateCount = existing.UpdateCount + 1
	}

	job := &OrderJob{
		Symbol:      s.Detail.Code,
		Sniper:      s,
		CancelID:    cancelID,
		OrderPtr:    orderPtr,
		Priority:    priority,
		UpdateCount: updateCount,
		RequestedAt: time.Now(),
	}

	u.pendingJobs[s.Detail.Code] = job
	u.jobMu.Unlock()
}

// dispatchWorker は秒間10回（安全のため110ms間隔）のペースでキューから最も重要なジョブを取り出して実行します
func (u *TradeUseCase) dispatchWorker(ctx context.Context) {
	ticker := time.NewTicker(110 * time.Millisecond) // 約9回/秒
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			job := u.pickBestJob()
			if job != nil {
				go u.executeJob(ctx, job)
			}
		}
	}
}

func (u *TradeUseCase) pickBestJob() *OrderJob {
	u.jobMu.Lock()
	defer u.jobMu.Unlock()

	if len(u.pendingJobs) == 0 {
		return nil
	}

	// 全ジョブをリスト化してソート
	var jobs []*OrderJob
	for _, j := range u.pendingJobs {
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
	delete(u.pendingJobs, best.Symbol)
	return best
}

func (u *TradeUseCase) executeJob(ctx context.Context, job *OrderJob) {
	// API通信用のタイムアウト付きコンテキスト
	apiCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if job.CancelID != "" {
		fmt.Printf("🛑 [%s] 注文キャンセルを実行中 (ID: %s, Priority: %d)\n", job.Symbol, job.CancelID, job.Priority)
		if err := u.gateway.CancelOrder(apiCtx, job.CancelID); err != nil {
			fmt.Printf("❌ [%s] キャンセル失敗: %v\n", job.Symbol, err)
		}
		return
	}

	if job.OrderPtr != nil {
		fmt.Printf("🚀 [%s] 発注を実行中 (%s %.0f株 @%.1f, Priority: %d)\n", job.Symbol, job.OrderPtr.Action, job.OrderPtr.OrderQty, job.OrderPtr.OrderPrice, job.Priority)
		updatedOrder, err := u.gateway.SendOrder(apiCtx, *job.OrderPtr)
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

func (u *TradeUseCase) GetSnipers() []*sniper.Sniper {
	return u.snipers
}

func (u *TradeUseCase) GetDataPool() tick.DataPool {
	return u.dataPool
}

type AggregatedPerformance struct {
	Name          string
	Trades        int
	Wins          int
	Losses        int
	RealizedPnL   float64
	UnrealizedPnL float64
}

// PrintPerformanceReport summarizes and prints the performance of all snipers.
func (u *TradeUseCase) PrintPerformanceReport(enableCSV bool) {

	// キー: "Symbol|StrategyName"
	perfMap := make(map[string]*AggregatedPerformance)
	symPerfMap := make(map[string]*AggregatedPerformance)
	stratPerfMap := make(map[string]*AggregatedPerformance)
	totalPerf := &AggregatedPerformance{Name: "Total"}

	for _, s := range u.snipers {
		stratName := s.Strategy.Name()
		key := s.Detail.Code + "|" + stratName

		if perfMap[key] == nil {
			perfMap[key] = &AggregatedPerformance{Name: strings.Replace(key, "|", " x ", 1)}
		}
		if symPerfMap[s.Detail.Code] == nil {
			symPerfMap[s.Detail.Code] = &AggregatedPerformance{Name: s.Detail.Code}
		}
		if stratPerfMap[stratName] == nil {
			stratPerfMap[stratName] = &AggregatedPerformance{Name: stratName}
		}

		// 含み損益の計算
		var unrealized float64
		marketState := u.dataPool.GetState(s.Detail.Code)
		if !marketState.LatestTick.CurrentPriceTime.IsZero() {
			latestPrice := marketState.LatestTick.Price
			unrealized = s.CalcUnrealizedPnL(latestPrice)
		}

		// 成績を集計
		updatePerf := func(p *AggregatedPerformance) {
			p.Trades += s.Performance.Trades
			p.Wins += s.Performance.Wins
			p.Losses += s.Performance.Losses
			p.RealizedPnL += s.Performance.RealizedPnL
			p.UnrealizedPnL += unrealized // 最新の含み損益を使用
		}

		updatePerf(perfMap[key])
		updatePerf(symPerfMap[s.Detail.Code])
		updatePerf(stratPerfMap[stratName])
		updatePerf(totalPerf)
	}

	printPerf := func(name string, p *AggregatedPerformance) {
		winRate := 0.0
		if p.Trades > 0 {
			winRate = float64(p.Wins) / float64(p.Trades) * 100
		}
		fmt.Printf("%-20s | 取引: %4d回 | 勝率: %5.1f%% (%4d勝 %4d敗) | 実現損益: %+10.0f 円 | 含み損益: %+10.0f 円 | 合計: %+10.0f 円\n",
			name, p.Trades, winRate, p.Wins, p.Losses, p.RealizedPnL, p.UnrealizedPnL, p.RealizedPnL+p.UnrealizedPnL)
	}

	fmt.Println("\n=============================================")
	fmt.Println("             トレード成績サマリー")
	fmt.Println("=============================================")

	fmt.Println("\n=== 1. 全体成績 (Total Performance) ===")
	printPerf("Total", totalPerf)

	fmt.Println("\n=== 2. 銘柄別成績 (Performance by Symbol) ===")
	for _, p := range symPerfMap {
		printPerf(p.Name, p)
	}

	fmt.Println("\n=== 3. ストラテジー別成績 (Performance by Strategy) ===")
	for _, p := range stratPerfMap {
		printPerf(p.Name, p)
	}

	fmt.Println("\n=== 4. 銘柄 × ストラテジー相性 (Performance by Symbol + Strategy) ===")
	for _, p := range perfMap {
		printPerf(p.Name, p)
	}
	fmt.Println("=============================================")

	if enableCSV {
		u.saveToCSV(totalPerf, symPerfMap, stratPerfMap, perfMap)
	}
}

func (u *TradeUseCase) saveToCSV(total *AggregatedPerformance, symbols map[string]*AggregatedPerformance, strats map[string]*AggregatedPerformance, combined map[string]*AggregatedPerformance) {
	outputDir := "data"
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		fmt.Printf("❌ ディレクトリ作成失敗: %v\n", err)
		return
	}

	now := time.Now().Format("20060102_150405")
	filename := fmt.Sprintf("performance_%s.csv", now)
	fullpath := filepath.Join(outputDir, filename)

	file, err := os.Create(fullpath)
	if err != nil {
		fmt.Printf("❌ CSV作成失敗: %v\n", err)
		return
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	// ヘッダー
	writer.Write([]string{"Type", "Name", "Trades", "Wins", "Losses", "WinRate", "RealizedPnL", "UnrealizedPnL", "TotalPnL"})

	writeLine := func(t, name string, p *AggregatedPerformance) {
		winRate := 0.0
		if p.Trades > 0 {
			winRate = float64(p.Wins) / float64(p.Trades) * 100
		}
		writer.Write([]string{
			t,
			name,
			strconv.Itoa(p.Trades),
			strconv.Itoa(p.Wins),
			strconv.Itoa(p.Losses),
			fmt.Sprintf("%.1f", winRate),
			fmt.Sprintf("%.0f", p.RealizedPnL),
			fmt.Sprintf("%.0f", p.UnrealizedPnL),
			fmt.Sprintf("%.0f", p.RealizedPnL+p.UnrealizedPnL),
		})
	}

	writeLine("Total", total.Name, total)
	for _, p := range symbols {
		writeLine("Symbol", p.Name, p)
	}
	for _, p := range strats {
		writeLine("Strategy", p.Name, p)
	}
	for _, p := range combined {
		writeLine("SymbolStrategy", p.Name, p)
	}

	fmt.Printf("💾 成績をCSVに保存しました: %s\n", fullpath)
}
