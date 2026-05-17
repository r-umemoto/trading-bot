package service

import (
	"context"
	"fmt"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/ord"
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

// CleanupOnStartup は起動時に残存している「注文」と「建玉」をすべてクリーンアップします
func (c *PositionCleaner) CleanupOnStartup(ctx context.Context) error {
	fmt.Println("🧹 起動時のシステム状態チェックを開始します...")

	// 1. 未約定の注文をすべてキャンセル
	fmt.Println("🔍 未約定注文の確認...")
	orders, err := c.marketGateway.GetOrders(ctx)
	if err != nil {
		fmt.Printf("⚠️ 注文取得エラー (スキップします): %v\n", err)
	} else {
		for _, o := range orders.Orders {
			if !o.IsCompleted() && o.Symbol != "" {
				fmt.Printf("🛑 前回の残存注文をキャンセルします: %s %s @%.1f\n", o.Symbol, o.Action, o.OrderPrice)
				if err := c.marketGateway.CancelOrder(ctx, o.ID); err != nil {
					fmt.Printf("❌ キャンセル失敗 (ID: %s): %v\n", o.ID, err)
				}
			}
		}
	}

	// 2. 建玉の強制決済
	fmt.Println("🔍 残存建玉の確認...")
	initialPositions, err := c.marketGateway.GetPositions(ctx, ord.PRODUCT_MARGIN)
	if err != nil {
		return fmt.Errorf("建玉取得エラー: %w", err)
	}

	cleaned := false
	for _, pos := range initialPositions {
		if pos.LeavesQty > 0 {
			fmt.Printf("🔥 前回の残存建玉を発見。成行で強制決済します: %s %f株\n", pos.Symbol, pos.LeavesQty)

			action := ord.ACTION_SELL
			if pos.Action == ord.ACTION_SELL {
				action = ord.ACTION_BUY
			}

			order := ord.NewOrderPtr(ord.GenerateLocalID(), pos.Symbol, action, 0, pos.LeavesQty)
			order.Exchange = pos.Exchange
			order.SecurityType = ord.SECURITY_TYPE_STOCK
			order.MarginTradeType = pos.TradeType
			order.AccountType = pos.AccountType
			order.ClosePositionOrder = ord.CLOSE_POSITION_ASC_DAY_DEC_PL
			order.OrderType = ord.ORDER_TYPE_MARKET

			updatedOrder, err := c.marketGateway.SendOrder(ctx, *order)
			if err != nil {
				return fmt.Errorf("強制決済の発注エラー (%s): %w", pos.Symbol, err)
			}
			*order = updatedOrder
			cleaned = true
		}
	}

	if cleaned {
		fmt.Println("⏳ クリーンアップの約定処理を待機中 (3秒)...")
		time.Sleep(3 * time.Second)

		finalPositions, err := c.marketGateway.GetPositions(ctx, ord.PRODUCT_MARGIN)
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
			if !cancel.IsCompleted() {
				fmt.Printf("🛑 [%s] 注文(ID: %s)をキャンセル中...\n", s.Detail.Code, cancel.ID)
				err := c.marketGateway.CancelOrder(ctx, cancel.ID)
				if err != nil {
					fmt.Printf("❌ [%s] キャンセルエラー: %v\n", s.Detail.Code, err)
				} else {
					cancel.Status = ord.ORDER_STATUS_CANCELED // キャンセル完了として扱う
				}
				// 🌟 連射を避けるために少し待機
				time.Sleep(200 * time.Millisecond)
			}
		}
	}

	// 🌟 キャンセルが浸透するまで少し待つ
	time.Sleep(1 * time.Second)

	// --- 第二段階：証券会社側でのロック解除を待機しつつ、全決済を完遂する ---
	safety := 0
	for {
		fmt.Println("🔍 最終ポジション確認を実行します...")
		positions, err := c.marketGateway.GetPositions(ctx, ord.PRODUCT_MARGIN)
		if err != nil {
			fmt.Printf("❌ 建玉取得エラー: %v\n", err)
		} else {
			remainingCount := 0
			for _, pos := range positions {
				if pos.LeavesQty > 0 {
					remainingCount++
					fmt.Printf("⚠️ 警告: 建玉が残っています！ 銘柄: %s, 数量: %f, 状態: %s\n", pos.Symbol, pos.LeavesQty, pos.Action)

					// 反対売買の方向を決定
					action := ord.ACTION_SELL
					if pos.Action == ord.ACTION_SELL {
						action = ord.ACTION_BUY
					}

					fmt.Printf("🔥 成行で強制決済を試みます: %s (%s)\n", pos.Symbol, action)

					order := ord.NewOrderPtr(ord.GenerateLocalID(), pos.Symbol, action, 0, pos.LeavesQty)
					order.Exchange = pos.Exchange
					order.SecurityType = ord.SECURITY_TYPE_STOCK
					order.MarginTradeType = pos.TradeType
					order.AccountType = pos.AccountType
					order.ClosePositionOrder = ord.CLOSE_POSITION_ASC_DAY_DEC_PL
					order.OrderType = ord.ORDER_TYPE_MARKET

					updatedOrder, err := c.marketGateway.SendOrder(ctx, *order)
					if err != nil {
						fmt.Printf("❌ [%s] 強送決済エラー: %v\n", pos.Symbol, err)
					} else {
						*order = updatedOrder
					}
					// 🌟 連射を避けるために少し待機
					time.Sleep(200 * time.Millisecond)
				}
			}

			if remainingCount == 0 {
				fmt.Println("✅ 【完全勝利】すべての建玉の決済が確認されました。ノーポジションです。システムを安全にシャットダウンします。")
				return nil
			}

			fmt.Printf("🚨 【緊急事態】未決済の建玉が %d 件残っています！\n", remainingCount)
		}

		safety++
		if safety > 5 { // リトライ回数を少し増やす
			fmt.Println("🔄 リトライ上限に達しました。")
			break
		}

		fmt.Println("🔄 10秒後に強制決済プロセスをリトライします...")
		time.Sleep(10 * time.Second)
	}

	fmt.Println("⚠️ 警告: 一部の建玉が未決済のままですが、システムを終了します。持ち越しリスクに注意してください。")
	return nil
}
