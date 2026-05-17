// cmd/bot/engine.go
package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
	"github.com/r-umemoto/trading-bot/pkg/infra/kabu/api"
)

// UseCaseHandler はシステムライフサイクルと取引実行を統合的に調整する唯一の窓口となるインターフェースです
type UseCaseHandler interface {
	Initialize(ctx context.Context) error
	Shutdown(ctx context.Context) error
	ExecuteTick(ctx context.Context, t tick.Tick)
	ExecuteExecutionReport(ctx context.Context, report order.Orders, symbol string)
	PrintReport(enableCSV bool)
	GetSymbols() []string
}

// Engine はシステム全体のライフサイクル（並行実行管理、実行ループ）を管理するホストコンテナです
type Engine struct {
	gateway       market.MarketGateway
	usecase       UseCaseHandler
	tickChannels  map[string]chan tick.Tick    // 銘柄ごとのTick処理チャネル
	orderChannels map[string]chan order.Orders // 銘柄ごとの注文レポートチャネル

	client      *api.KabuClient // クリーンアップと最終確認用
	apiPassword string
}

func NewEngine(gateway market.MarketGateway, usecase UseCaseHandler) *Engine {
	e := &Engine{
		gateway:       gateway,
		usecase:       usecase,
		tickChannels:  make(map[string]chan tick.Tick),
		orderChannels: make(map[string]chan order.Orders),
	}

	// 銘柄ごとに並行処理用のチャネルを作成
	for _, symbol := range usecase.GetSymbols() {
		if _, exists := e.tickChannels[symbol]; !exists {
			// バッファサイズを拡張 (100 -> 1000) してスパイク耐性を高める
			e.tickChannels[symbol] = make(chan tick.Tick, 1000)
			e.orderChannels[symbol] = make(chan order.Orders, 1000)
		}
	}

	return e
}

// Run はシステムの初期化を行い、メインループを開始します
func (e *Engine) Run(ctx context.Context) error {
	// 1. システムの初期化（残存クリーンアップ、銘柄登録、ディスパッチャ起動などを丸ごと委ねる）
	if err := e.usecase.Initialize(ctx); err != nil {
		return err
	}

	// 2. 市場接続（WebSocket/Polling）の開始
	priceCh, execCh, err := e.gateway.Start(ctx)
	if err != nil {
		return err
	}

	// 3. 各銘柄のTick処理ワーカー（Goroutine）を起動
	e.StartWorkers(ctx)

	// 時間指定キルスイッチ用のタイマー（1秒周期）
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	fmt.Println("🚀 市場の監視を開始しました。メインループに入ります。")

	// 4. メインループ
Loop:
	for {
		select {
		case <-ctx.Done(): // OS of 終了シグナル (Ctrl+C)
			fmt.Println("\n🚨 システム終了シグナルを検知！監視ループを停止します...")
			break Loop

		case t := <-ticker.C: // 時間の監視
			if t.Hour() == 15 && t.Minute() >= 15 {
				fmt.Println("\n⏰【キルスイッチ作動】指定時刻到達。全スナイパーに撤収を命じます！")
				break Loop
			}

		case tick := <-priceCh: // 価格の受信
			e.HandleTick(ctx, tick)
		case report := <-execCh:
			// 約定通知が来たら、担当のスナイパーを探して渡す（ルーティング）
			e.HandleExecution(report)
		}
	}

	// 5. システムのシャットダウンプロセス（全ポジションクローズ、登録解除など）
	fmt.Println("🏁 システムのシャットダウンプロセスを開始します...")
	return e.usecase.Shutdown(ctx)
}

// StartWorkers は銘柄ごとのワーカー（Goroutine）を起動します
func (e *Engine) StartWorkers(ctx context.Context) {
	for symbol := range e.tickChannels {
		go e.worker(ctx, symbol, e.tickChannels[symbol], e.orderChannels[symbol])
	}
}

// worker は特定の銘柄のTickや約定通知を専用に処理するGoroutineです
func (e *Engine) worker(ctx context.Context, symbol string, tickCh <-chan tick.Tick, orderCh <-chan order.Orders) {
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-tickCh:
			// 100% 同期的にユースケースを呼び出す
			e.usecase.ExecuteTick(ctx, t)
		case report := <-orderCh:
			// 100% 同期的にユースケースを呼び出す
			e.usecase.ExecuteExecutionReport(ctx, report, symbol)
		}
	}
}

// HandleTick は市場のTickデータを受け取り、該当銘柄のチャネルへルーティングします
func (e *Engine) HandleTick(ctx context.Context, tick tick.Tick) {
	if ch, ok := e.tickChannels[tick.Symbol]; ok {
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
func (e *Engine) HandleExecution(report order.Orders) {
	// 全銘柄分を個別にルーティングするのは効率が悪いが、現状の構造に合わせる
	for symbol, ch := range e.orderChannels {
		select {
		case ch <- report:
		default:
			// 🚨 約定通知は絶対に破棄してはいけない（状態がズレるため）。
			// ただし、メインスレッドをブロックすると全銘柄に波及するため、非同期で喜びを運ぶ。
			fmt.Printf("⚠️ 🚨 [%s] OrdersReportチャネルがフルです。非同期でキューイングを試みます。\n", symbol)
			go func(c chan<- order.Orders, r order.Orders) {
				c <- r // バックグラウンドでブロックして待つ
			}(ch, report)
		}
	}
}

func (e *Engine) PrintReport(enableCSV bool) {
	e.usecase.PrintReport(enableCSV)
}
