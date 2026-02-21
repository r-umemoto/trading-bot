// cmd/bot/engine.go
package main

import (
	"context"
	"fmt"
	"time"

	"trading-bot/internal/domain/market"
	"trading-bot/internal/domain/sniper"
	"trading-bot/internal/infra/kabu"
)

// Engine はシステム全体のライフサイクル（初期化、実行、停止）を管理する司令部です
type Engine struct {
	streamer    market.PriceStreamer
	snipers     []*sniper.Sniper
	client      *kabu.KabuClient // クリーンアップと最終確認用
	apiPassword string
}

func NewEngine(streamer market.PriceStreamer, snipers []*sniper.Sniper, client *kabu.KabuClient, apiPassword string) *Engine {
	return &Engine{
		streamer:    streamer,
		snipers:     snipers,
		client:      client,
		apiPassword: apiPassword,
	}
}

// Run はシステムの初期化を行い、メインループを開始します
func (e *Engine) Run(ctx context.Context) error {
	// 1. 起動時のクリーンアップ（Engineの管轄）
	if err := e.cleanupInitialPositions(); err != nil {
		return fmt.Errorf("起動時クリーンアップ失敗: %w", err)
	}

	// 2. 価格配信の購読開始
	symbols := make([]string, 0, len(e.snipers))
	for _, s := range e.snipers {
		symbols = append(symbols, s.Symbol)
	}
	tickCh, err := e.streamer.Subscribe(ctx, symbols)
	if err != nil {
		return fmt.Errorf("価格配信の購読に失敗: %w", err)
	}

	// 3. 時間指定キルスイッチ用のタイマー（1秒周期）
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

		case tick := <-tickCh: // 価格の受信
			for _, s := range e.snipers {
				if s.Symbol == tick.Symbol {
					s.OnPriceUpdate(tick.Price)
				}
			}
		}
	}

	// 5. ループを抜けた後の死に際の処理
	return e.shutdown()
}

// shutdown は全スナイパーに撤退命令を出して、完全にノーポジになるまで執念深く確認します
func (e *Engine) shutdown() error {
	fmt.Println("\n🚨 全スナイパーに緊急撤退命令を出します...")
	for _, s := range e.snipers {
		s.ForceExit() // スナイパー自身の未約定キャンセルと成行決済を実行
	}

	fmt.Println("⏳ 撤収完了。取引所の約定データ反映を待機中 (3秒)...")
	time.Sleep(3 * time.Second)

	for {
		fmt.Println("🔍 最終ポジション確認を実行します...")
		finalPositions, err := e.client.GetPositions("2")

		if err == nil {
			remainingCount := 0
			for _, pos := range finalPositions {
				if pos.LeavesQty > 0 {
					remainingCount++
					fmt.Printf("⚠️ 警告: 建玉が残っています！ 銘柄: %s, 残数量: %f\n", pos.SymbolName, pos.LeavesQty)
				}
			}

			if remainingCount == 0 {
				fmt.Println("✅ 【完全勝利】すべての建玉の決済が確認されました。ノーポジションです。システムを安全にシャットダウンします。")
				return nil
			}

			fmt.Printf("🚨 【緊急事態】未決済の建玉が %d 件残っています！\n", remainingCount)
		} else {
			fmt.Printf("❌ 最終確認での建玉取得エラー: %v\n", err)
		}

		fmt.Println("🔄 30秒後に強制決済プロセスをリトライします...")
		time.Sleep(30 * time.Second)
	}
}

// cleanupInitialPositions は起動時に残存している建玉をすべて成行で強制決済します
func (e *Engine) cleanupInitialPositions() error {
	fmt.Println("🧹 起動時のシステム状態チェックを開始します...")

	initialPositions, err := e.client.GetPositions("2")
	if err != nil {
		return fmt.Errorf("建玉取得エラー: %w", err)
	}

	cleaned := false
	for _, pos := range initialPositions {
		if pos.LeavesQty > 0 {
			qty := int(pos.LeavesQty)
			fmt.Printf("🔥 前回の残存建玉を発見。成行で強制決済します: %s %d株\n", pos.SymbolName, qty)

			req := kabu.OrderRequest{
				Password:       e.apiPassword,
				Symbol:         pos.Symbol,
				Exchange:       1,
				SecurityType:   1,
				Side:           "1", // 売
				Qty:            qty,
				FrontOrderType: 10, // 成行
				Price:          0,
			}
			if _, err := e.client.SendOrder(req); err != nil {
				return fmt.Errorf("強制決済の発注エラー (%s): %w", pos.SymbolName, err)
			}
			cleaned = true
		}
	}

	if cleaned {
		fmt.Println("⏳ クリーンアップの約定処理を待機中 (3秒)...")
		time.Sleep(3 * time.Second)

		finalPositions, err := e.client.GetPositions("2")
		if err != nil {
			return fmt.Errorf("最終確認での建玉取得エラー: %w", err)
		}
		for _, pos := range finalPositions {
			if pos.LeavesQty > 0 {
				return fmt.Errorf("🚨 クリーンアップ後も建玉が残っています (%s: %f株)。手動で確認してください", pos.SymbolName, pos.LeavesQty)
			}
		}
		fmt.Println("✅ クリーンアップ完了。システムはノーポジションから開始します。")
	} else {
		fmt.Println("✅ 残存建玉はありません。クリーンな状態で起動します。")
	}

	return nil
}
