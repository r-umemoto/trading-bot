package service

import (
	"context"
	"fmt"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
)

// PositionCleaner はシステムの起動・終了時に、不要な建玉を強制決済してお掃除するサービスです。
type PositionCleaner struct {
	snipers []*sniper.Sniper
	broker  market.MarketGateway
}

func NewPositionCleaner(snipers []*sniper.Sniper, broker market.MarketGateway) *PositionCleaner {
	return &PositionCleaner{
		snipers: snipers,
		broker:  broker,
	}
}

// CleanupOnStartup は起動時に残存している建玉をすべて成行で強制決済します
func (c *PositionCleaner) CleanupOnStartup(ctx context.Context) error {
	fmt.Println("🧹 起動時のシステム状態チェックを開始します...")

	initialPositions, err := c.broker.GetPositions(ctx, market.PRODUCT_MARGIN)
	if err != nil {
		return fmt.Errorf("建玉取得エラー: %w", err)
	}

	cleaned := false
	for _, pos := range initialPositions {
		if pos.LeavesQty > 0 {
			fmt.Printf("🔥 前回の残存建玉を発見。成行で強制決済します: %s %f株\n", pos.Symbol, pos.LeavesQty)

			req := market.OrderRequest{
				Symbol:             pos.Symbol,
				Exchange:           pos.Exchange,
				SecurityType:       market.SECURITY_TYPE_STOCK,
				Action:             market.ACTION_SELL,
				MarginTradeType:    pos.TradeType,
				AccountType:        pos.AccountType,
				ClosePositionOrder: market.CLOSE_POSITION_ASC_DAY_DEC_PL,
				OrderType:          market.ORDER_TYPE_MARKET,
				Qty:                pos.LeavesQty,
				Price:              0,
			}
			if _, err := c.broker.SendOrder(ctx, req); err != nil {
				return fmt.Errorf("強制決済の発注エラー (%s): %w", pos.Symbol, err)
			}
			cleaned = true
		}
	}

	if cleaned {
		fmt.Println("⏳ クリーンアップの約定処理を待機中 (3秒)...")
		time.Sleep(3 * time.Second)

		finalPositions, err := c.broker.GetPositions(ctx, market.PRODUCT_MARGIN)
		if err != nil {
			return fmt.Errorf("最終確認での建玉取得エラー: %w", err)
		}
		for _, pos := range finalPositions {
			if pos.LeavesQty > 0 {
				return fmt.Errorf("🚨 クリーンアップ後も建玉が残っています (%s: %f株)。手動で確認してください", pos.Symbol, pos.LeavesQty)
			}
		}
		fmt.Println("✅ クリーンアップ完了。システムはノーポジションから開始します。")
	} else {
		fmt.Println("✅ 残存建玉はありません。クリーンな状態で起動します。")
	}

	return nil
}

// CleanAllPositions は終了時に全スナイパーを撤収させ、ノーポジになるまで見届けます
func (c *PositionCleaner) CleanAllPositions(ctx context.Context) error {
	fmt.Println("\n🚨 全スナイパーに緊急撤退命令を出します...")

	for _, s := range c.snipers {
		s.ForceExit()
		for _, cancel := range s.Orders {
			if !cancel.IsCanceled {
				fmt.Printf("🛑 [%s] 注文(ID: %s)をキャンセル中...\n", s.Symbol, cancel.ID)
				err := c.broker.CancelOrder(ctx, cancel.ID)
				if err != nil {
					fmt.Printf("❌ [%s] キャンセルエラー: %v\n", s.Symbol, err)
				} else {
					cancel.IsCanceled = true // キャンセル完了として扱う
				}
			}
		}
	}

	// --- 第二段階：証券会社側でのロック解除を待機 ---
	time.Sleep(2 * time.Second)

	positions, err := c.broker.GetPositions(ctx, market.PRODUCT_MARGIN)
	if err != nil {
		fmt.Printf("❌ 建玉取得エラー: %v\n", err)
		return nil
	}

	for _, ramainOrder := range positions {
		// 成り行きで売る
		c.broker.SendOrder(ctx, market.OrderRequest{
			Symbol: ramainOrder.Symbol,
			Action: market.ACTION_SELL,
			Qty:    ramainOrder.LeavesQty,
		})
	}

	fmt.Println("⏳ 撤収完了。取引所の約定データ反映を待機中 (3秒)...")
	time.Sleep(3 * time.Second)

	safety := 0
	for {
		fmt.Println("🔍 最終ポジション確認を実行します...")
		remainPpsitions, err := c.broker.GetPositions(ctx, market.PRODUCT_MARGIN)
		if err == nil {
			remainingCount := 0
			for _, pos := range remainPpsitions {
				if pos.LeavesQty > 0 {
					remainingCount++
					fmt.Printf("⚠️ 警告: 建玉が残っています！ 銘柄: %s, 残数量: %f\n", pos.Symbol, pos.LeavesQty)
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
		safety++
		if safety > 2 {
			fmt.Println("🔄 リトライ上限...")
			break
		}
	}
	return fmt.Errorf("異常終了")
}
