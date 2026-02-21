// cmd/bot/engine.go
package main

import (
	"context"
	"fmt"
	"time"
	"trading-bot/internal/domain/market"
	"trading-bot/internal/domain/sniper"
)

// Engine はシステム全体のライフサイクル（実行と停止）を管理する司令部です
type Engine struct {
	streamer market.PriceStreamer
	snipers  []*sniper.Sniper
}

func NewEngine(streamer market.PriceStreamer, snipers []*sniper.Sniper) *Engine {
	return &Engine{
		streamer: streamer,
		snipers:  snipers,
	}
}

// Run はシステムのメインループを開始し、ctxがキャンセルされるまでブロックします
func (e *Engine) Run(ctx context.Context) error {
	// 1. 価格配信の購読開始
	// ※ 監視対象の銘柄一覧をスナイパーから抽出して渡す
	symbols := make([]string, 0, len(e.snipers))
	for _, s := range e.snipers {
		symbols = append(symbols, s.Symbol)
	}

	tickCh, err := e.streamer.Subscribe(ctx, symbols)
	if err != nil {
		return fmt.Errorf("価格配信の購読に失敗: %w", err)
	}

	fmt.Println("🚀 市場の監視を開始します...")

	// 2. メインループ
Loop:
	for {
		select {
		case <-ctx.Done():
			fmt.Println("\n🚨 システム終了シグナルを検知！監視ループを停止します...")
			break Loop

		case tick := <-tickCh:
			// 受け取った価格を各スナイパーに分配
			for _, s := range e.snipers {
				if s.Symbol == tick.Symbol {
					s.OnPriceUpdate(tick.Price)
				}
			}
		}
	}

	// 3. ループを抜けた後の死に際の処理（Graceful Shutdown）
	return e.shutdown()
}

// shutdown は全スナイパーに撤退命令を出して終了を待ちます
func (e *Engine) shutdown() error {
	fmt.Println("\n🚨 全スナイパーに緊急撤退命令を出します...")
	for _, s := range e.snipers {
		s.EmergencyExit()
	}

	fmt.Println("⏳ 撤退注文の通信完了を待機中 (3秒)...")
	time.Sleep(3 * time.Second)
	fmt.Println("システムを安全にシャットダウンします。")
	return nil
}
