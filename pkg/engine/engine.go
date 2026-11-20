// cmd/bot/engine.go
package engine

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
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
	usecase  UseCaseHandler
	cleanups []func()
}

func NewEngine(usecase UseCaseHandler, cleanups ...func()) *Engine {
	return &Engine{
		usecase:  usecase,
		cleanups: cleanups,
	}
}

// Run はシステムの起動を行い、時刻監視とメインスレッド待機を開始します
func (e *Engine) Run(ctx context.Context) error {
	// 1. バックグラウンドワーカー（ディスパッチャ、WebSocket、ポーリング等）用のコンテキストを準備します。
	// これらは OS シグナル（Ctrl+C）受信時も即座に停止せず、シャットダウン処理完了後に安全に停止するようにライフサイクルを分離します。
	bgCtx, bgCancel := context.WithCancel(context.Background())
	defer bgCancel()

	// 2. システムの起動
	if err := e.usecase.Start(bgCtx); err != nil {
		return err
	}

	// 3. 時刻監視用のキルスイッチコンテキストを構築（15:15で自動キャンセル）
	killCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go e.monitorKillSwitch(killCtx, cancel)

	// 4. メインスレッドの待機（Ctrl+Cによる強制終了、または15:15のキルスイッチによるキャンセルまでここでブロック）
	fmt.Println("🚀 リアルタイム監視ストリームを監視中...")
	<-killCtx.Done()

	// 5. システムのシャットダウンプロセス（全ポジションクローズ、登録解除など）
	fmt.Println("🏁 システムのシャットダウンプロセスを開始します...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer shutdownCancel()
	err := e.usecase.Shutdown(shutdownCtx)

	// 6. シャットダウン完了後にバックグラウンドワーカーを安全に停止します
	bgCancel()

	// 7. ログのフラッシュなどリソースのクリーンアップを実行します
	for _, cleanup := range e.cleanups {
		if cleanup != nil {
			cleanup()
		}
	}

	return err
}

// ResolveKillSwitchTime は環境変数 BOT_EXIT_TIME ("HH:MM", デフォルト "15:32") から終了時刻を返します。
func ResolveKillSwitchTime() (int, int) {
	raw := strings.TrimSpace(os.Getenv("BOT_EXIT_TIME"))
	if raw == "" {
		return 15, 32
	}
	parts := strings.Split(raw, ":")
	if len(parts) != 2 {
		return 15, 32
	}
	h, errH := strconv.Atoi(parts[0])
	m, errM := strconv.Atoi(parts[1])
	if errH != nil || errM != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 15, 32
	}
	return h, m
}

// monitorKillSwitch は取引終了時刻（BOT_EXIT_TIME、デフォルト 15:32 JST）を監視し、到達時にコンテキストをキャンセルします
func (e *Engine) monitorKillSwitch(ctx context.Context, cancel context.CancelFunc) {
	exitHour, exitMinute := ResolveKillSwitchTime()
	jst := time.FixedZone("JST", 9*3600)

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			tj := t.In(jst)
			if tj.Hour() > exitHour || (tj.Hour() == exitHour && tj.Minute() >= exitMinute) {
				fmt.Printf("\n⏰【キルスイッチ作動】指定時刻 (%02d:%02d) 到達。全スナイパーに撤収を命じます！\n", exitHour, exitMinute)
				cancel()
				return
			}
		}
	}
}

func (e *Engine) PrintReport(enableCSV bool) {
	e.usecase.PrintReport(enableCSV)
}
