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
	ForceExit(ctx context.Context) error
	Shutdown(ctx context.Context) error
	PrintReport(enableCSV bool)
}

// Engine はシステム全体のライフサイクル（起動、強制撤退、終了、キルスイッチ監視）を統括するホストコンテナです
type Engine struct {
	usecase       UseCaseHandler
	cleanups      []func()
	nowFunc       func() time.Time
	checkInterval time.Duration
}

func NewEngine(usecase UseCaseHandler, cleanups ...func()) *Engine {
	return &Engine{
		usecase:       usecase,
		cleanups:      cleanups,
		nowFunc:       time.Now,
		checkInterval: 1 * time.Second,
	}
}

// Run はシステムの起動を行い、時刻監視（強制撤退・システム終了）とメインスレッド待機を開始します
func (e *Engine) Run(ctx context.Context) error {
	// 1. バックグラウンドワーカー（ディスパッチャ、WebSocket、ポーリング等）用のコンテキストを準備します。
	// これらは OS シグナル（Ctrl+C）受信時も即座に停止せず、シャットダウン処理完了後に安全に停止するようにライフサイクルを分離します。
	bgCtx, bgCancel := context.WithCancel(context.Background())
	defer bgCancel()

	// 2. システムの起動
	if err := e.usecase.Start(bgCtx); err != nil {
		return err
	}

	// 3. 時刻監視（フェーズ1: 強制撤退 / フェーズ2: システム終了キルスイッチ）を起動
	killCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go e.monitorSchedule(killCtx, cancel)

	// 4. メインスレッドの待機（Ctrl+Cによる強制終了、またはキルスイッチ時刻到達までここでブロック）
	fmt.Println("🚀 リアルタイム監視ストリームを監視中...")
	<-killCtx.Done()

	// 5. システムのシャットダウンプロセス（残存建玉の再確認・全決済、銘柄登録解除など）
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

func resolveTimeEnv(envKey string, defaultHour, defaultMinute int) (int, int) {
	raw := strings.TrimSpace(os.Getenv(envKey))
	if raw == "" {
		return defaultHour, defaultMinute
	}
	parts := strings.Split(raw, ":")
	if len(parts) != 2 {
		return defaultHour, defaultMinute
	}
	h, errH := strconv.Atoi(parts[0])
	m, errM := strconv.Atoi(parts[1])
	if errH != nil || errM != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return defaultHour, defaultMinute
	}
	return h, m
}

// ResolveForceExitTime は環境変数 BOT_FORCE_EXIT_TIME ("HH:MM", デフォルト "15:23") から強制撤退（建玉解消）時刻を返します。
func ResolveForceExitTime() (int, int) {
	return resolveTimeEnv("BOT_FORCE_EXIT_TIME", 15, 23)
}

// ResolveKillSwitchTime は環境変数 BOT_EXIT_TIME ("HH:MM", デフォルト "15:32") からシステム終了時刻を返します。
func ResolveKillSwitchTime() (int, int) {
	return resolveTimeEnv("BOT_EXIT_TIME", 15, 32)
}

func isTimeReached(t time.Time, hour, minute int) bool {
	return t.Hour() > hour || (t.Hour() == hour && t.Minute() >= minute)
}

// monitorSchedule は以下の2段階の時刻監視を行います。
//   - フェーズ1 (BOT_FORCE_EXIT_TIME, デフォルト 15:23 JST): 全スナイパーの取引を停止し、建玉を全決済します（WebSocket / TickData収集は継続）
//   - フェーズ2 (BOT_EXIT_TIME, デフォルト 15:32 JST): 大引け後のTick収集完了後にコンテキストをキャンセルし、システムを終了します
func (e *Engine) monitorSchedule(ctx context.Context, cancel context.CancelFunc) {
	forceExitHour, forceExitMinute := ResolveForceExitTime()
	exitHour, exitMinute := ResolveKillSwitchTime()
	jst := time.FixedZone("JST", 9*3600)

	interval := e.checkInterval
	if interval <= 0 {
		interval = 1 * time.Second
	}
	nowFn := e.nowFunc
	if nowFn == nil {
		nowFn = time.Now
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	forceExited := false

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tj := nowFn().In(jst)

			// フェーズ2: システム終了（キルスイッチ）
			if isTimeReached(tj, exitHour, exitMinute) {
				fmt.Printf("\n⏰【キルスイッチ作動】指定時刻 (%02d:%02d) 到達。システムを終了します！\n", exitHour, exitMinute)
				cancel()
				return
			}

			// フェーズ1: 強制撤退（取引停止＆建玉全決済のみ実行し、TickData収集は継続）
			if !forceExited && isTimeReached(tj, forceExitHour, forceExitMinute) {
				forceExited = true
				fmt.Printf("\n⏰【強制撤退作動】指定時刻 (%02d:%02d) 到達。全スナイパーを停止し建玉を解消します（Tickデータ収集は %02d:%02d まで継続）\n",
					forceExitHour, forceExitMinute, exitHour, exitMinute)
				go func() {
					forceExitCtx, forceExitCancel := context.WithTimeout(context.Background(), 2*time.Minute)
					defer forceExitCancel()
					if err := e.usecase.ForceExit(forceExitCtx); err != nil {
						fmt.Printf("⚠️ 強制撤退（ポジションクローズ）失敗: %v\n", err)
					}
				}()
			}
		}
	}
}

func (e *Engine) PrintReport(enableCSV bool) {
	e.usecase.PrintReport(enableCSV)
}
