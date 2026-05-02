package runner

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/r-umemoto/trading-bot/pkg/config"
	"github.com/r-umemoto/trading-bot/pkg/engine"
	"github.com/r-umemoto/trading-bot/pkg/portfolio"
)

// RunBot はBotの初期化と実行をカプセル化した関数です。
func RunBot() error {
	fmt.Println("システム起動: 初期化プロセスを開始します。")

	// 1. コンテキスト（OSシグナル）の準備
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 1. 設定の読み込み
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("設定の読み込みに失敗しました: %w", err)
	}

	// 2. ポートフォリオの読み込み
	portfolioPath := "configs/portfolio.json"
	if p := os.Getenv("PORTFOLIO_PATH"); p != "" {
		portfolioPath = p
	}
	targets, err := portfolio.LoadFromJSON(portfolioPath)
	if err != nil {
		return fmt.Errorf("ポートフォリオの読み込みに失敗しました: %w", err)
	}

	// 3. アプリケーションの組み立て
	e, err := engine.BuildEngine(ctx, cfg, targets)
	if err != nil {
		return fmt.Errorf("engineの組み立て失敗: %w", err)
	}

	// 5. 実行！（ここでブロックされ、Engineの内部ですべてが回る）
	if err := e.Run(ctx); err != nil {
		return fmt.Errorf("システム異常終了: %w", err)
	}

	// 6. 終了時に成績を表示（CSV出力も有効化）
	e.PrintReport(true)

	return nil
}
