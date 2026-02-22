package service

import (
	"fmt"
	"time"
	"trading-bot/internal/domain/sniper"
	"trading-bot/internal/infra/kabu"
)

// PositionCleaner はシステムの起動・終了時に、不要な建玉を強制決済してお掃除するサービスです。
type PositionCleaner struct {
	snipers     []*sniper.Sniper
	client      *kabu.KabuClient
	apiPassword string
}

func NewPositionCleaner(snipers []*sniper.Sniper, client *kabu.KabuClient, apiPassword string) *PositionCleaner {
	return &PositionCleaner{
		snipers:     snipers,
		client:      client,
		apiPassword: apiPassword,
	}
}

// CleanupOnStartup は起動時に残存している建玉をすべて成行で強制決済します
func (c *PositionCleaner) CleanupOnStartup() error {
	fmt.Println("🧹 起動時のシステム状態チェックを開始します...")

	initialPositions, err := c.client.GetPositions("2")
	if err != nil {
		return fmt.Errorf("建玉取得エラー: %w", err)
	}

	cleaned := false
	for _, pos := range initialPositions {
		if pos.LeavesQty > 0 {
			qty := int(pos.LeavesQty)
			fmt.Printf("🔥 前回の残存建玉を発見。成行で強制決済します: %s %d株\n", pos.SymbolName, qty)

			req := kabu.OrderRequest{
				Password:       c.apiPassword,
				Symbol:         pos.Symbol,
				Exchange:       1,
				SecurityType:   1,
				Side:           "1", // 売
				Qty:            qty,
				FrontOrderType: 10, // 成行
				Price:          0,
			}
			if _, err := c.client.SendOrder(req); err != nil {
				return fmt.Errorf("強制決済の発注エラー (%s): %w", pos.SymbolName, err)
			}
			cleaned = true
		}
	}

	if cleaned {
		fmt.Println("⏳ クリーンアップの約定処理を待機中 (3秒)...")
		time.Sleep(3 * time.Second)

		finalPositions, err := c.client.GetPositions("2")
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

// CleanAllPositions は終了時に全スナイパーを撤収させ、ノーポジになるまで見届けます
func (c *PositionCleaner) CleanAllPositions() error {
	fmt.Println("\n🚨 全スナイパーに緊急撤退命令を出します...")
	for _, s := range c.snipers {
		s.ForceExit()
	}

	fmt.Println("⏳ 撤収完了。取引所の約定データ反映を待機中 (3秒)...")
	time.Sleep(3 * time.Second)

	for {
		fmt.Println("🔍 最終ポジション確認を実行します...")
		finalPositions, err := c.client.GetPositions("2")

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
