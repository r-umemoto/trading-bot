// cmd/bot/engine.go
package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/service"
	"github.com/r-umemoto/trading-bot/pkg/infra/kabu/api"
	"github.com/r-umemoto/trading-bot/pkg/usecase"
)

// Engine はシステム全体のライフサイクル（初期化、実行、停止）を管理する司令部です
type Engine struct {
	gateway market.MarketGateway
	tradeUC *usecase.TradeUseCase
	cleaner *service.PositionCleaner

	client      *api.KabuClient // クリーンアップと最終確認用
	apiPassword string
}

func NewEngine(gateway market.MarketGateway, tradeUC *usecase.TradeUseCase, cleaner *service.PositionCleaner) *Engine {
	return &Engine{
		gateway: gateway,
		tradeUC: tradeUC,
		cleaner: cleaner,
	}
}

// Run はシステムの初期化を行い、メインループを開始します
func (e *Engine) Run(ctx context.Context) error {
	// 1. 起動時のクリーンアップ（残存注文・建玉の強制決済）
	if err := e.cleaner.CleanupOnStartup(ctx); err != nil {
		return err
	}

	// 2. 監視銘柄の登録 (PUSH APIの購読開始)
	if err := e.setupSubscriptions(ctx); err != nil {
		return err
	}
	// システム終了時に登録を全解除することを保証
	defer func() {
		fmt.Println("\n🧹 監視銘柄の登録を解除中...")
		if err := e.gateway.UnregisterSymbolAll(ctx); err != nil {
			fmt.Printf("⚠️ 銘柄登録解除に失敗: %v\n", err)
		}
	}()

	// 3. 市場接続（WebSocket/Polling）の開始
	priceCh, execCh, err := e.gateway.Start(ctx)
	if err != nil {
		return err
	}

	// 4. 各銘柄のTick処理ワーカーを起動
	e.tradeUC.StartWorkers(ctx)

	// 時間指定キルスイッチ用のタイマー（1秒周期）
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	fmt.Println("🚀 市場の監視を開始しました。メインループに入ります。")

	// 5. メインループ（すべてを1つのselectで統括する）
Loop:
	for {
		select {
		case <-ctx.Done(): // OSの終了シグナル (Ctrl+C)
			fmt.Println("\n🚨 システム終了シグナルを検知！監視ループを停止します...")
			break Loop

		case t := <-ticker.C: // 時間の監視
			if t.Hour() == 15 && t.Minute() >= 15 {
				fmt.Println("\n⏰【キルスイッチ作動】指定時刻到達。全スナイパーに撤収を命じます！")
				break Loop
			}

		case tick := <-priceCh: // 価格の受信
			e.tradeUC.HandleTick(ctx, tick)
		case report := <-execCh:
			// 約定通知が来たら、担当のスナイパーを探して渡す（ルーティング）
			e.tradeUC.HandleExecution(report)
		}
	}

	// 6. ループを抜けた後の死に際の処理（全ポジションのクローズ）
	fmt.Println("🏁 システムのシャットダウンプロセスを開始します...")
	return e.cleaner.CleanAllPositions(ctx)
}

func (e *Engine) setupSubscriptions(ctx context.Context) error {
	fmt.Println("📡 監視銘柄の登録を開始します...")

	snipers := e.tradeUC.GetSnipers()
	var reqs []market.ResisterSymbolRequest
	seen := make(map[string]bool)

	for _, s := range snipers {
		key := fmt.Sprintf("%s:%d", s.Detail.Code, s.Exchange)
		if seen[key] {
			continue
		}
		reqs = append(reqs, market.ResisterSymbolRequest{
			Symbol:   s.Detail.Code,
			Exchange: s.Exchange,
		})
		seen[key] = true
	}

	if err := e.gateway.RegisterSymbols(ctx, reqs); err != nil {
		return fmt.Errorf("監視銘柄の登録に失敗: %w", err)
	}

	return nil
}

func (e *Engine) PrintReport(enableCSV bool) {
	e.tradeUC.PrintPerformanceReport(enableCSV)
}
