package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/r-umemoto/trading-bot/pkg/config"
	"github.com/r-umemoto/trading-bot/pkg/engine"
)

func main() {
	fmt.Println("システム起動: 初期化プロセスを開始します。")

	// 1. コンテキスト（OSシグナル）の準備
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 1. 設定の読み込み（エラーチェックが自動で効く！）
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("❌ 設定の読み込みに失敗しました: %v", err)
	}

	// 3. アプリケーションの組み立て
	watchList := []engine.WatchTarget{
		{Symbol: "9434", StrategyName: "sample"},
	}
	engine, err := engine.BuildEngine(cfg, watchList)
	if err != nil {
		log.Fatalf("❌ engineの組み立て失敗: %v", err)
	}

	// 5. 実行！（ここでブロックされ、Engineの内部ですべてが回る）
	if err := engine.Run(ctx); err != nil {
		log.Fatalf("❌ システム異常終了: %v", err)
	}
}
