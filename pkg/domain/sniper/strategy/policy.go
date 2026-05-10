package strategy

import (
	"fmt"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
)

// ExecutionPolicy は疑似約定（Synthetic Fill）の判定ロジックを定義するインターフェースです
type ExecutionPolicy interface {
	ApplySyntheticFill(order *market.Order, tick market.Tick)
}

// TouchTTLPolicy は、価格が同値にタッチした瞬間に疑似約定と見なしますが、
// 指定されたTTL（有効期限）を超過しても約定通知が来ない場合は期待を解除します。
type TouchTTLPolicy struct {
	TTL time.Duration
}

func (p *TouchTTLPolicy) ApplySyntheticFill(order *market.Order, tick market.Tick) {
	if order.OrderPrice > 0 && tick.Price > 0 { // 指値の場合
		isTouching := (order.Action == market.ACTION_BUY && tick.Price <= order.OrderPrice) ||
			(order.Action == market.ACTION_SELL && tick.Price >= order.OrderPrice)

		if isTouching {
			if order.Status != market.ORDER_STATUS_FILL_EXPECTED && !order.TouchTimeout {
				// 初めてタッチした瞬間（フライング推測）
				order.Status = market.ORDER_STATUS_FILL_EXPECTED
				order.FillExpectedAt = tick.CurrentPriceTime
				if order.FillExpectedAt.IsZero() {
					order.FillExpectedAt = time.Now()
				}
				fmt.Printf("⚡ [%s] 疑似約定を検知しました (TTL計測開始): %s (Price: %f, Tick: %f)\n", order.Symbol, order.ID, order.OrderPrice, tick.Price)
			} else if order.Status == market.ORDER_STATUS_FILL_EXPECTED {
				// すでに推測中：TTLの超過チェック
				elapsed := tick.CurrentPriceTime.Sub(order.FillExpectedAt)
				if elapsed > p.TTL {
					order.Status = market.ORDER_STATUS_WAITING
					order.TouchTimeout = true // これ以降、価格が離れるまでは再推測しない
					fmt.Printf("💔 [%s] 疑似約定がタイムアウトしました（キュー負け）: %s\n", order.Symbol, order.ID)
				}
			}
		} else {
			// 価格が離れた場合：すべての推測・タイムアウト状態をリセット
			if order.Status == market.ORDER_STATUS_FILL_EXPECTED || order.TouchTimeout {
				order.Status = market.ORDER_STATUS_WAITING
				order.TouchTimeout = false
				order.FillExpectedAt = time.Time{}
				fmt.Printf("🔄 [%s] 価格が離れたため疑似約定ステータスをリセットしました: %s\n", order.Symbol, order.ID)
			}
		}
	}
}

// StrictPiercePolicy は、価格が完全に指値を貫通（< または >）した時のみ疑似約定と見なします。
// タッチしただけでは疑似約定としません。
type StrictPiercePolicy struct{}

func (p *StrictPiercePolicy) ApplySyntheticFill(order *market.Order, tick market.Tick) {
	if order.OrderPrice > 0 && tick.Price > 0 {
		isPierced := (order.Action == market.ACTION_BUY && tick.Price < order.OrderPrice) ||
			(order.Action == market.ACTION_SELL && tick.Price > order.OrderPrice)

		if isPierced {
			if order.Status != market.ORDER_STATUS_FILL_EXPECTED {
				order.Status = market.ORDER_STATUS_FILL_EXPECTED
				fmt.Printf("⚡ [%s] 貫通による確実な疑似約定を検知しました: %s (Price: %f, Tick: %f)\n", order.Symbol, order.ID, order.OrderPrice, tick.Price)
			}
		} else {
			if order.Status == market.ORDER_STATUS_FILL_EXPECTED {
				order.Status = market.ORDER_STATUS_WAITING
				fmt.Printf("🔄 [%s] 価格が戻ったため貫通約定ステータスをリセットしました: %s\n", order.Symbol, order.ID)
			}
		}
	}
}

// NoopPolicy は疑似約定判定を一切行いません（Observer戦略など向け）。
type NoopPolicy struct{}

func (p *NoopPolicy) ApplySyntheticFill(order *market.Order, tick market.Tick) {
	// 何もしない
}
