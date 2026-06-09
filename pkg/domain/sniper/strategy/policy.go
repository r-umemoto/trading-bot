package strategy

import (
	"fmt"
	"math"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/brain"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

// ExecutionPolicy は疑似約定（Synthetic Fill）の判定ロジックを定義するインターフェースです
type ExecutionPolicy interface {
	ApplySyntheticFill(ord *order.Order, tick tick.Tick)
	// IsOrderDesired は、現在の注文が戦略の意図（sig）と実質的に一致しているか（維持すべきか）を判定します。
	// これにより、微細な価格変化によるキャンセル・再発注のスパムを抑制します。
	IsOrderDesired(ord *order.Order, sig brain.Signal, symbol symbol.Symbol) bool
}

// TouchTTLPolicy は、価格が同値にタッチした瞬間に疑似約定と見なしますが、
// 指定されたTTL（有効期限）を超過しても約定通知が来ない場合は期待を解除します。
type TouchTTLPolicy struct {
	TTL time.Duration
}

func (p *TouchTTLPolicy) ApplySyntheticFill(ord *order.Order, tick tick.Tick) {
	if ord.IsCancelSent() || ord.IsCompleted() {
		return
	}
	if ord.OrderPrice > 0 && tick.Price > 0 { // 指値の場合
		isTouching := (ord.Action == order.ACTION_BUY && tick.Price <= ord.OrderPrice) ||
			(ord.Action == order.ACTION_SELL && tick.Price >= ord.OrderPrice)

		if isTouching {
			if !ord.IsFillExpected() && !ord.Synthetic.TouchTimeout {
				// 初めてタッチした瞬間（フライング推測）
				ord.ToFillExpected()
				ord.Synthetic.ExpectedAt = tick.CurrentPriceTime
				if ord.Synthetic.ExpectedAt.IsZero() {
					ord.Synthetic.ExpectedAt = time.Now()
				}
				fmt.Printf("⚡ [%s] 疑似約定を検知しました (TTL計測開始): %s (Price: %f, Tick: %f)\n", ord.Symbol, ord.ID, ord.OrderPrice, tick.Price)
			} else if ord.IsFillExpected() {
				// すでに推測中：TTLの超過チェック
				elapsed := tick.CurrentPriceTime.Sub(ord.Synthetic.ExpectedAt)
				if elapsed > p.TTL {
					ord.ToWaiting()
					ord.Synthetic.TouchTimeout = true // これ以降、価格が離れるまでは再推測しない
					fmt.Printf("💔 [%s] 疑似約定がタイムアウトしました（キュー負け）: %s\n", ord.Symbol, ord.ID)
				}
			}
		} else {
			// 価格が離れた場合：すべての推測・タイムアウト状態をリセット
			if ord.IsFillExpected() || ord.Synthetic.TouchTimeout {
				if ord.IsFillExpected() {
					ord.ToWaiting()
				}
				ord.Synthetic.TouchTimeout = false
				ord.Synthetic.ExpectedAt = time.Time{}
				fmt.Printf("🔄 [%s] 価格が離れたため疑似約定ステータスをリセットしました: %s\n", ord.Symbol, ord.ID)
			}
		}
	}
}

func (p *TouchTTLPolicy) IsOrderDesired(ord *order.Order, sig brain.Signal, symbol symbol.Symbol) bool {
	return isOrderDesiredDefault(ord, sig, symbol)
}

// StrictPiercePolicy は、価格が完全に指値を貫通（< または >）した時のみ疑似約定と見なします。
// タッチしただけでは疑似約定としません。
type StrictPiercePolicy struct{}

func (p *StrictPiercePolicy) ApplySyntheticFill(ord *order.Order, tick tick.Tick) {
	if ord.IsCancelSent() || ord.IsCompleted() {
		return
	}
	if ord.OrderPrice > 0 && tick.Price > 0 {
		isPierced := (ord.Action == order.ACTION_BUY && tick.Price < ord.OrderPrice) ||
			(ord.Action == order.ACTION_SELL && tick.Price > ord.OrderPrice)

		if isPierced {
			if !ord.IsFillExpected() {
				ord.ToFillExpected()
				fmt.Printf("⚡ [%s] 貫通による確実な疑似約定を検知しました: %s (Price: %f, Tick: %f)\n", ord.Symbol, ord.ID, ord.OrderPrice, tick.Price)
			}
		} else {
			if ord.IsFillExpected() {
				ord.ToWaiting()
				fmt.Printf("🔄 [%s] 価格が戻ったため貫通約定ステータスをリセットしました: %s\n", ord.Symbol, ord.ID)
			}
		}
	}
}

func (p *StrictPiercePolicy) IsOrderDesired(ord *order.Order, sig brain.Signal, symbol symbol.Symbol) bool {
	return isOrderDesiredDefault(ord, sig, symbol)
}

// VolumeConsumptionPolicy は、板の厚み（待ち行列）と出来高の消化量に基づいて約定を推測します。
// タッチした瞬間の板の枚数を記録し、その後の出来高がその枚数を超えたら約定と見なします。
type VolumeConsumptionPolicy struct {
	// QueueOffsetRatio は、待ち行列の何割が消化されたら約定と見なすかの比率です (0.0 - 1.0)
	// 1.0 (100%) だと保守的、0.8 (80%) だとやや攻撃的です。
	QueueOffsetRatio float64
}

func (p *VolumeConsumptionPolicy) ApplySyntheticFill(ord *order.Order, tick tick.Tick) {
	if ord.IsCancelSent() || ord.IsCompleted() {
		return
	}
	if ord.OrderPrice <= 0 || tick.Price <= 0 {
		return
	}

	// 1. 貫通判定（指値価格を突き抜けた場合は即座に約定確定）
	isPierced := (ord.Action == order.ACTION_BUY && tick.Price < ord.OrderPrice) ||
		(ord.Action == order.ACTION_SELL && tick.Price > ord.OrderPrice)

	if isPierced {
		if !ord.IsFillExpected() {
			ord.ToFillExpected()
			ord.Synthetic.ExpectedAt = tick.CurrentPriceTime
			fmt.Printf("⚡ [%s] 疑似約定(貫通)を検知しました: %s\n", ord.Symbol, ord.ID)
		}
		return
	}

	// 2. 同値タッチ判定
	isTouching := tick.Price == ord.OrderPrice

	if isTouching {
		// 初期状態の記録（初めて同値に触れた瞬間の板の厚みを「自分の前の待ち行列」とする）
		if ord.Synthetic.InitialQueueQty == 0 {
			if ord.Action == order.ACTION_BUY {
				ord.Synthetic.InitialQueueQty = tick.BestBid.Qty
			} else {
				ord.Synthetic.InitialQueueQty = tick.BestAsk.Qty
			}
			ord.Synthetic.LastVolumeUpdate = tick.TradingVolume
			ord.Synthetic.ConsumedVolume = 0
			fmt.Printf("📝 [%s] 待ち行列の監視を開始: %s (Queue: %.0f)\n", ord.Symbol, ord.ID, ord.Synthetic.InitialQueueQty)
			return
		}

		// 出来高の増分を計算して、キューの消化量に加算
		if ord.Synthetic.LastVolumeUpdate > 0 && tick.TradingVolume > ord.Synthetic.LastVolumeUpdate {
			deltaVol := tick.TradingVolume - ord.Synthetic.LastVolumeUpdate
			ord.Synthetic.ConsumedVolume += deltaVol
		}
		ord.Synthetic.LastVolumeUpdate = tick.TradingVolume

		// 消化量が閾値（自分の順番）を超えたかチェック
		if !ord.IsFillExpected() {
			threshold := ord.Synthetic.InitialQueueQty * p.QueueOffsetRatio
			if ord.Synthetic.ConsumedVolume >= threshold {
				ord.ToFillExpected()
				ord.Synthetic.ExpectedAt = tick.CurrentPriceTime
				if ord.Synthetic.ExpectedAt.IsZero() {
					ord.Synthetic.ExpectedAt = time.Now()
				}
				fmt.Printf("⚡ [%s] 疑似約定(出来高消化)を検知しました: %s (Consumed: %.0f / Queue: %.0f)\n",
					ord.Symbol, ord.ID, ord.Synthetic.ConsumedVolume, ord.Synthetic.InitialQueueQty)
			}
		} else {
			// すでに疑似約定状態：安全のためのタイムアウト（2秒など）
			elapsed := tick.CurrentPriceTime.Sub(ord.Synthetic.ExpectedAt)
			if elapsed > 2*time.Second {
				ord.ToWaiting()
				ord.Synthetic.TouchTimeout = true // これ以降、価格が離れるまでは再推測しない
				fmt.Printf("💔 [%s] 疑似約定(出来高)がタイムアウトしました（幻の約定）: %s\n", ord.Symbol, ord.ID)
			}
		}
	} else {
		// 価格が離れている間も、総出来高の同期だけは維持する
		ord.Synthetic.LastVolumeUpdate = tick.TradingVolume
	}
}

func (p *VolumeConsumptionPolicy) IsOrderDesired(ord *order.Order, sig brain.Signal, symbol symbol.Symbol) bool {
	// スキャルピング等の高頻度戦略を想定し、1ティック以内の変化なら維持する
	return isOrderDesiredDefault(ord, sig, symbol)
}

// NoopPolicy は疑似約定判定を一切行いません（Observer戦略など向け）。
type NoopPolicy struct{}

func (p *NoopPolicy) ApplySyntheticFill(ord *order.Order, tick tick.Tick) {
	// 何もしない
}

func (p *NoopPolicy) IsOrderDesired(ord *order.Order, sig brain.Signal, symbol symbol.Symbol) bool {
	// 常に再判定を促す（実質的に利用されない）
	return false
}

// --- ヘルパー関数 ---

// isOrderDesiredDefault は「方向・数量が一致」かつ「価格差が1ティック以内」なら維持とみなすデフォルト判定です。
func isOrderDesiredDefault(ord *order.Order, sig brain.Signal, symbol symbol.Symbol) bool {
	marketAction, _ := sig.Action.ToMarketAction()
	if ord.Action != marketAction || ord.OrderQty != sig.Quantity {
		return false
	}

	// NaN 同士の比較は一致とみなす
	if math.IsNaN(sig.Price) && math.IsNaN(ord.OrderPrice) {
		return true
	}
	if math.IsNaN(sig.Price) || math.IsNaN(ord.OrderPrice) {
		return false
	}

	// 指値価格の比較
	if sig.Price > 0 && ord.OrderPrice > 0 {
		tickSize := symbol.CalcTickSize(ord.OrderPrice)
		if ord.Action == order.ACTION_BUY {
			// 1. 既にシグナル価格以上の指値なら、より約定しやすく、
			// かつ取引所の価格改善（指値以下での約定）も期待できるため、あえてキャンセルしない。
			if ord.OrderPrice >= sig.Price {
				return true
			}
			// 2. シグナルより低い価格の場合、1ティック以内なら許容（スパム防止）。
			// それ以上低いと「買えない（買えなくなった）」ため、キャンセルしてシグナルに合わせる。
			return (sig.Price - ord.OrderPrice) <= (tickSize + 0.0001)
		} else {
			// 1. 既にシグナル価格以下の指値なら、より約定しやすく、
			// かつ価格改善（指値以上での約定）も期待できるため維持。
			if ord.OrderPrice <= sig.Price {
				return true
			}
			// 2. シグナルより高い価格の場合、1ティック以内なら許容。
			// それ以上高いと「売れない（売れなくなった）」ためキャンセル。
			return (ord.OrderPrice - sig.Price) <= (tickSize + 0.0001)
		}
	}

	// 成行同士（Price=0）なら一致
	return sig.Price == 0 && ord.OrderPrice == 0
}
