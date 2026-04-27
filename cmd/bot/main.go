package main

import (
	"log"

	"github.com/r-umemoto/trading-bot/pkg/runner"
)

func main() {
	if err := runner.RunBot(); err != nil {
		log.Fatalf("❌ システム異常終了: %v", err)
	}
}
