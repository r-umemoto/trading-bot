package main

import (
	"log"

	"github.com/r-umemoto/trading-bot/pkg/runner"
)

func main() {
	if err := runner.RunBacktest(); err != nil {
		log.Fatalf("❌ バックテスト異常終了: %v", err)
	}
}
