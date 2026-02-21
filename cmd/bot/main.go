package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"trading-bot/internal/infra/kabu"
)

func main() {
	fmt.Println("システム起動: 初期化プロセスを開始します。")

	// 1. コンテキスト（OSシグナル）の準備
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 2. インフラ（APIクライアント）の準備
	apiPassword := os.Getenv("KABU_API_PASSWORD")
	if apiPassword == "" {
		apiPassword = "dummy_password"
	}
	client := kabu.NewKabuClient("http://localhost:18080/kabusapi", "")

	if err := client.GetToken(apiPassword); err != nil {
		log.Fatalf("❌ トークン取得エラー: %v", err)
	}
	fmt.Println("✅ APIトークン取得完了")

	// 3. アプリケーションの組み立て（portfolio.go の buildPortfolio を呼び出す）
	engine := buildPortfolio(client, apiPassword)

	// 5. 実行！（ここでブロックされ、Engineの内部ですべてが回る）
	if err := engine.Run(ctx); err != nil {
		log.Fatalf("❌ システム異常終了: %v", err)
	}
}
