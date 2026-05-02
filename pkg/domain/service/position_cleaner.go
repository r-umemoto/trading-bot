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
	snipers       []*sniper.Sniper
	marketGateway market.MarketGateway
}

func NewPositionCleaner(snipers []*sniper.Sniper, marketGateway market.MarketGateway) *PositionCleaner {
	return &PositionCleaner{
		snipers:       snipers,
		marketGateway: marketGateway,
	}
}

// CleanupOnStartup は起動時に残存している建玉をすべて成行で強制決済します
func (c *PositionCleaner) CleanupOnStartup(ctx context.Context) error {
	fmt.Println("🧹 起動時のシステム状態チェックを開始します...")

	initialPositions, err := c.marketGateway.GetPositions(ctx, market.PRODUCT_MARGIN)
	if err != nil {
		return fmt.Errorf("建玉取得エラー: %w", err)
	}

	cleaned := false
	for _, pos := range initialPositions {
		if pos.LeavesQty > 0 {
			fmt.Printf("🔥 前回の残存建玉を発見。成行で強制決済します: %s %f株\n", pos.Symbol, pos.LeavesQty)

			action := market.ACTION_SELL
			if pos.Action == market.ACTION_SELL {
				action = market.ACTION_BUY
			}

			req := market.OrderRequest{
				Symbol:             pos.Symbol,
				Exchange:           pos.Exchange,
				SecurityType:       market.SECURITY_TYPE_STOCK,
				Action:             action,
				MarginTradeType:    pos.TradeType,
				AccountType:        pos.AccountType,
				ClosePositionOrder: market.CLOSE_POSITION_ASC_DAY_DEC_PL,
				OrderType:          market.ORDER_TYPE_MARKET,
				Qty:                pos.LeavesQty,
				Price:              0,
			}
			if _, err := c.marketGateway.SendOrder(ctx, req); err != nil {
				return fmt.Errorf("強制決済の発注エラー (%s): %w", pos.Symbol, err)
			}
			cleaned = true
		}
	}

	if cleaned {
		fmt.Println("⏳ クリーンアップの約定処理を待機中 (3秒)...")
		time.Sleep(3 * time.Second)

		finalPositions, err := c.marketGateway.GetPositions(ctx, market.PRODUCT_MARGIN)
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

	// 監視銘柄を登録　TODOいったん仮でここに実装
	registered := make(map[string]bool)
	for _, s := range c.snipers {
		key := fmt.Sprintf("%s:%d", s.Detail.Code, s.Exchange)
		if registered[key] {
			continue
		}

		req := market.ResisterSymbolRequest{
			Symbol:   s.Detail.Code,
			Exchange: s.Exchange,
		}
		err := c.marketGateway.RegisterSymbol(ctx, req)
		if err != nil {
			return fmt.Errorf("銘柄登録失敗: %s", s.Detail.Code)
		}
		fmt.Printf("✅ 銘柄登録 %s \n", s.Detail.Code)
		registered[key] = true

		// APIの秒間上限を回避するため1秒スリープ
		time.Sleep(1 * time.Second)
	}

	return nil
}

// CleanAllPositions は終了時に全スナイパーを撤収させ、ノーポジになるまで見届けます
func (c *PositionCleaner) CleanAllPositions(ctx context.Context) error {

	// 銘柄解除 TODO いったんここで
	fmt.Println("\n🚨 銘柄登録全解除")
	c.marketGateway.UnregisterSymbolAll(ctx)

	fmt.Println("\n🚨 全スナイパーに緊急撤退命令を出します...")

	for _, s := range c.snipers {
		s.ForceExit()
		for _, cancel := range s.Orders {
			if !cancel.IsCompleted() {
				fmt.Printf("🛑 [%s] 注文(ID: %s)をキャンセル中...\n", s.Detail.Code, cancel.ID)
				err := c.marketGateway.CancelOrder(ctx, cancel.ID)
				if err != nil {
					fmt.Printf("❌ [%s] キャンセルエラー: %v\n", s.Detail.Code, err)
				} else {
					cancel.Status = market.ORDER_STATUS_CANCELED // キャンセル完了として扱う
				}
			}
		}
	}

	// --- 第二段階：証券会社側でのロック解除を待機しつつ、全決済を完遂する ---
	safety := 0
	for {
		fmt.Println("🔍 最終ポジション確認を実行します...")
		positions, err := c.marketGateway.GetPositions(ctx, market.PRODUCT_MARGIN)
		if err != nil {
			fmt.Printf("❌ 建玉取得エラー: %v\n", err)
		} else {
			remainingCount := 0
			for _, pos := range positions {
				if pos.LeavesQty > 0 {
					remainingCount++
					fmt.Printf("⚠️ 警告: 建玉が残っています！ 銘柄: %s, 数量: %f, 状態: %s\n", pos.Symbol, pos.LeavesQty, pos.Action)

					// 反対売買の方向を決定
					action := market.ACTION_SELL
					if pos.Action == market.ACTION_SELL {
						action = market.ACTION_BUY
					}

					fmt.Printf("🔥 成行で強制決済を試みます: %s (%s)\n", pos.Symbol, action)

					req := market.OrderRequest{
						Symbol:             pos.Symbol,
						Exchange:           pos.Exchange,
						SecurityType:       market.SECURITY_TYPE_STOCK,
						Action:             action,
						MarginTradeType:    pos.TradeType,
						AccountType:        pos.AccountType,
						ClosePositionOrder: market.CLOSE_POSITION_ASC_DAY_DEC_PL,
						OrderType:          market.ORDER_TYPE_MARKET,
						Qty:                pos.LeavesQty,
						Price:              0,
					}
					_, err := c.marketGateway.SendOrder(ctx, req)
					if err != nil {
						fmt.Printf("❌ [%s] 強送決済エラー: %v\n", pos.Symbol, err)
					}
				}
			}

			if remainingCount == 0 {
				fmt.Println("✅ 【完全勝利】すべての建玉の決済が確認されました。ノーポジションです。システムを安全にシャットダウンします。")
				return nil
			}

			fmt.Printf("🚨 【緊急事態】未決済の建玉が %d 件残っています！\n", remainingCount)
		}

		safety++
		if safety > 3 {
			fmt.Println("🔄 リトライ上限に達しました。")
			break
		}

		fmt.Println("🔄 10秒後に強制決済プロセスをリトライします...")
		time.Sleep(10 * time.Second)
	}

	fmt.Println("⚠️ 警告: 一部の建玉が未決済のままですが、システムを終了します。持ち越しリスクに注意してください。")
	return nil
}
