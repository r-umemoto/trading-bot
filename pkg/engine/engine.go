// cmd/bot/engine.go
package engine

import (
	"context"
	"fmt"
	"time"

	"trading-bot/pkg/domain/market"
	"trading-bot/pkg/domain/service"
	"trading-bot/pkg/infra/kabu"
	"trading-bot/pkg/usecase"
)

// Engine はシステム全体のライフサイクル（初期化、実行、停止）を管理する司令部です
type Engine struct {
	gateway      market.MarketGateway
	tradeUC      *usecase.TradeUseCase
	cleaner      *service.PositionCleaner
	watchSymbols []string

	client      *kabu.KabuClient // クリーンアップと最終確認用
	apiPassword string
}

func NewEngine(gateway market.MarketGateway, tradeUC *usecase.TradeUseCase, cleaner *service.PositionCleaner, watchSymbols []string) *Engine {
	return &Engine{
		gateway:      gateway,
		tradeUC:      tradeUC,
		cleaner:      cleaner,
		watchSymbols: watchSymbols,
	}
}

// Run はシステムの初期化を行い、メインループを開始します
func (e *Engine) Run(ctx context.Context) error {
	// ノーポジションに強制
	if err := e.cleaner.CleanupOnStartup(ctx); err != nil {
		return err
	}

	// 起動時処理をユースケースに移譲
	priceCh, execCh, err := e.gateway.Start(ctx)
	if err != nil {
		return err
	}

	// 時間指定キルスイッチ用のタイマー（1秒周期）
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	fmt.Println("🚀 市場の監視を開始します...")

	// 4. メインループ（すべてを1つのselectで統括する）
Loop:
	for {
		select {
		case <-ctx.Done(): // OSの終了シグナル (Ctrl+C)
			fmt.Println("\n🚨 システム終了シグナルを検知！監視ループを停止します...")
			break Loop

		case t := <-ticker.C: // 時間の監視
			if (t.Hour() == 14 && t.Minute() >= 50) || t.Hour() >= 315 {
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

	// 5. ループを抜けた後の死に際の処理
	return e.cleaner.CleanAllPositions(ctx)
}
