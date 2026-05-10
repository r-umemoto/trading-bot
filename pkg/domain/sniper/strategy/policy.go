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
			if order.Status != market.ORDER_STATUS_FILL_EXPECTED && !order.Synthetic.TouchTimeout {
				// 初めてタッチした瞬間（フライング推測）
				order.Status = market.ORDER_STATUS_FILL_EXPECTED
				order.Synthetic.ExpectedAt = tick.CurrentPriceTime
				if order.Synthetic.ExpectedAt.IsZero() {
					order.Synthetic.ExpectedAt = time.Now()
				}
				fmt.Printf("⚡ [%s] 疑似約定を検知しました (TTL計測開始): %s (Price: %f, Tick: %f)\n", order.Symbol, order.ID, order.OrderPrice, tick.Price)
			} else if order.Status == market.ORDER_STATUS_FILL_EXPECTED {
				// すでに推測中：TTLの超過チェック
				elapsed := tick.CurrentPriceTime.Sub(order.Synthetic.ExpectedAt)
				if elapsed > p.TTL {
					order.Status = market.ORDER_STATUS_WAITING
					order.Synthetic.TouchTimeout = true // これ以降、価格が離れるまでは再推測しない
					fmt.Printf("💔 [%s] 疑似約定がタイムアウトしました（キュー負け）: %s\n", order.Symbol, order.ID)
				}
			}
		} else {
			// 価格が離れた場合：すべての推測・タイムアウト状態をリセット
			if order.Status == market.ORDER_STATUS_FILL_EXPECTED || order.Synthetic.TouchTimeout {
				order.Status = market.ORDER_STATUS_WAITING
				order.Synthetic.TouchTimeout = false
				order.Synthetic.ExpectedAt = time.Time{}
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

// VolumeConsumptionPolicy は、板の厚み（待ち行列）と出来高の消化量に基づいて約定を推測します。
// タッチした瞬間の板の枚数を記録し、その後の出来高がその枚数を超えたら約定と見なします。
type VolumeConsumptionPolicy struct {
	// QueueOffsetRatio は、待ち行列の何割が消化されたら約定と見なすかの比率です (0.0 - 1.0)
	// 1.0 (100%) だと保守的、0.8 (80%) だとやや攻撃的です。
	QueueOffsetRatio float64
}

func (p *VolumeConsumptionPolicy) ApplySyntheticFill(order *market.Order, tick market.Tick) {
	if order.OrderPrice <= 0 || tick.Price <= 0 {
		return
	}

	// 1. 貫通判定（指値価格を突き抜けた場合は即座に約定確定）
	isPierced := (order.Action == market.ACTION_BUY && tick.Price < order.OrderPrice) ||
		(order.Action == market.ACTION_SELL && tick.Price > order.OrderPrice)

	if isPierced {
		if order.Status != market.ORDER_STATUS_FILL_EXPECTED {
			order.Status = market.ORDER_STATUS_FILL_EXPECTED
			order.Synthetic.ExpectedAt = tick.CurrentPriceTime
			fmt.Printf("⚡ [%s] 疑似約定(貫通)を検知しました: %s\n", order.Symbol, order.ID)
		}
		return
	}

	// 2. 同値タッチ判定
	isTouching := tick.Price == order.OrderPrice

	if isTouching {
		// 初期状態の記録（初めて同値に触れた瞬間の板の厚みを「自分の前の待ち行列」とする）
		if order.Synthetic.InitialQueueQty == 0 {
			if order.Action == market.ACTION_BUY {
				order.Synthetic.InitialQueueQty = tick.BestBid.Qty
			} else {
				order.Synthetic.InitialQueueQty = tick.BestAsk.Qty
			}
			order.Synthetic.LastVolumeUpdate = tick.TradingVolume
			order.Synthetic.ConsumedVolume = 0
			fmt.Printf("📝 [%s] 待ち行列の監視を開始: %s (Queue: %.0f)\n", order.Symbol, order.ID, order.Synthetic.InitialQueueQty)
			return
		}

		// 出来高の増分を計算して、キューの消化量に加算
		if order.Synthetic.LastVolumeUpdate > 0 && tick.TradingVolume > order.Synthetic.LastVolumeUpdate {
			deltaVol := tick.TradingVolume - order.Synthetic.LastVolumeUpdate
			order.Synthetic.ConsumedVolume += deltaVol
		}
		order.Synthetic.LastVolumeUpdate = tick.TradingVolume

		// 消化量が閾値（自分の順番）を超えたかチェック
		if order.Status != market.ORDER_STATUS_FILL_EXPECTED {
			threshold := order.Synthetic.InitialQueueQty * p.QueueOffsetRatio
			if order.Synthetic.ConsumedVolume >= threshold {
				order.Status = market.ORDER_STATUS_FILL_EXPECTED
				order.Synthetic.ExpectedAt = tick.CurrentPriceTime
				fmt.Printf("⚡ [%s] 疑似約定(出来高消化)を検知しました: %s (Consumed: %.0f / Queue: %.0f)\n",
					order.Symbol, order.ID, order.Synthetic.ConsumedVolume, order.Synthetic.InitialQueueQty)
			}
		}
	} else {
		// 価格が離れている間も、総出来高の同期だけは維持する（戻ってきた時の計算が狂わないように）
		order.Synthetic.LastVolumeUpdate = tick.TradingVolume
	}
}

// NoopPolicy は疑似約定判定を一切行いません（Observer戦略など向け）。
type NoopPolicy struct{}

func (p *NoopPolicy) ApplySyntheticFill(order *market.Order, tick market.Tick) {
	// 何もしない
}
