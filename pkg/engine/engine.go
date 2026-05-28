// cmd/bot/engine.go
package engine

import (
	"context"
	"fmt"
	"time"
)

// UseCaseHandler はシステムライフサイクルと取引実行を統合的に調整する唯一の窓口となるインターフェースです
type UseCaseHandler interface {
	Start(ctx context.Context) error
	Shutdown(ctx context.Context) error
	PrintReport(enableCSV bool)
}

// Engine はシステム全体のライフサイクル（起動、終了、キルスイッチ監視）を統括するホストコンテナです
type Engine struct {
	usecase UseCaseHandler
}

func NewEngine(usecase UseCaseHandler) *Engine {
	return &Engine{
		usecase: usecase,
	}
}

// Run はシステムの起動を行い、時刻監視とメインスレッド待機を開始します
func (e *Engine) Run(ctx context.Context) error {
	// 1. システムの起動（残存クリーンアップ、銘柄登録、ディスパッチャ起動、ストリーミング開始）
	if err := e.usecase.Start(ctx); err != nil {
		return err
	}

	// 2. 時刻監視用のキルスイッチコンテキストを構築（15:15で自動キャンセル）
	killCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go e.monitorKillSwitch(killCtx, cancel)

	// 3. メインスレッドの待機（Ctrl+Cによる強制終了、または15:15のキルスイッチによるキャンセルまでここでブロック）
	fmt.Println("🚀 リアルタイム監視ストリームを監視中...")
	<-killCtx.Done()

	// 4. システムのシャットダウンプロセス（全ポジションクローズ、登録解除など）
	fmt.Println("🏁 システムのシャットダウンプロセスを開始します...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer shutdownCancel()
	return e.usecase.Shutdown(shutdownCtx)
}

// monitorKillSwitch は取引終了時刻（15:15）を監視し、到達時にコンテキストをキャンセルします
func (e *Engine) monitorKillSwitch(ctx context.Context, cancel context.CancelFunc) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			if t.Hour() == 15 && t.Minute() >= 15 {
				fmt.Println("\n⏰【キルスイッチ作動】指定時刻到達。全スナイパーに撤収を命じます！")
				cancel()
				return
			}
		}
	}
}

func (e *Engine) PrintReport(enableCSV bool) {
	e.usecase.PrintReport(enableCSV)
}
