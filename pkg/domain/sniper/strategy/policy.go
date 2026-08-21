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

// ExecutionPolicy は疑似約定（Synthetic Fill）の判定および再評価ロジックを定義するインターフェースです
type ExecutionPolicy interface {
	ShouldFill(ord *order.Order, t tick.Tick) bool
	ShouldTimeout(ord *order.Order, t tick.Tick) bool
	ShouldReset(ord *order.Order, t tick.Tick) bool
	UpdateState(ord *order.Order, t tick.Tick)

	// IsOrderDesired は、現在の注文が戦略の意図（sig）と実質的に一致しているか（維持すべきか）を判定します。
	// これにより、微細な価格変化によるキャンセル・再発注のスパムを抑制します。
	IsOrderDesired(ord *order.Order, sig brain.Signal, symbol symbol.Symbol) bool
}

// TouchTTLPolicy は、価格が同値にタッチした瞬間に疑似約定と見なしますが、
// 指定されたTTL（有効期限）を超過しても約定通知が来ない場合は期待を解除します。
type TouchTTLPolicy struct {
	TTL time.Duration
}

func (p *TouchTTLPolicy) ShouldFill(ord *order.Order, t tick.Tick) bool {
	if ord.OrderPrice <= 0 || t.Price <= 0 {
		return false
	}
	isTouching := (ord.Action == order.ACTION_BUY && t.Price <= ord.OrderPrice) ||
		(ord.Action == order.ACTION_SELL && t.Price >= ord.OrderPrice)
	return isTouching && !ord.IsFillExpected() && !ord.Synthetic.TouchTimeout
}

func (p *TouchTTLPolicy) ShouldTimeout(ord *order.Order, t tick.Tick) bool {
	if ord.OrderPrice <= 0 || t.Price <= 0 {
		return false
	}
	isTouching := (ord.Action == order.ACTION_BUY && t.Price <= ord.OrderPrice) ||
		(ord.Action == order.ACTION_SELL && t.Price >= ord.OrderPrice)
	if !isTouching || !ord.IsFillExpected() {
		return false
	}
	elapsed := t.CurrentPriceTime.Sub(ord.Synthetic.ExpectedAt)
	return elapsed > p.TTL
}

func (p *TouchTTLPolicy) ShouldReset(ord *order.Order, t tick.Tick) bool {
	if ord.OrderPrice <= 0 || t.Price <= 0 {
		return false
	}
	isTouching := (ord.Action == order.ACTION_BUY && t.Price <= ord.OrderPrice) ||
		(ord.Action == order.ACTION_SELL && t.Price >= ord.OrderPrice)
	return !isTouching && (ord.IsFillExpected() || ord.Synthetic.TouchTimeout)
}

func (p *TouchTTLPolicy) UpdateState(ord *order.Order, t tick.Tick) {}

func (p *TouchTTLPolicy) IsOrderDesired(ord *order.Order, sig brain.Signal, symbol symbol.Symbol) bool {
	return isOrderDesiredDefault(ord, sig, symbol)
}

// StrictPiercePolicy は、価格が完全に指値を貫通（< または >）した時のみ疑似約定と見なします。
// タッチしただけでは疑似約定としません。
type StrictPiercePolicy struct{}

func (p *StrictPiercePolicy) ShouldFill(ord *order.Order, t tick.Tick) bool {
	if ord.OrderPrice <= 0 || t.Price <= 0 {
		return false
	}
	isPierced := (ord.Action == order.ACTION_BUY && t.Price < ord.OrderPrice) ||
		(ord.Action == order.ACTION_SELL && t.Price > ord.OrderPrice)
	return isPierced && !ord.IsFillExpected()
}

func (p *StrictPiercePolicy) ShouldTimeout(ord *order.Order, t tick.Tick) bool {
	return false
}

func (p *StrictPiercePolicy) ShouldReset(ord *order.Order, t tick.Tick) bool {
	if ord.OrderPrice <= 0 || t.Price <= 0 {
		return false
	}
	isPierced := (ord.Action == order.ACTION_BUY && t.Price < ord.OrderPrice) ||
		(ord.Action == order.ACTION_SELL && t.Price > ord.OrderPrice)
	return !isPierced && ord.IsFillExpected()
}

func (p *StrictPiercePolicy) UpdateState(ord *order.Order, t tick.Tick) {}

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

func (p *VolumeConsumptionPolicy) ShouldFill(ord *order.Order, t tick.Tick) bool {
	if ord.OrderPrice <= 0 || t.Price <= 0 {
		return false
	}

	// 1. 貫通判定（指値価格を突き抜けた場合は即座に約定確定）
	isPierced := (ord.Action == order.ACTION_BUY && t.Price < ord.OrderPrice) ||
		(ord.Action == order.ACTION_SELL && t.Price > ord.OrderPrice)
	if isPierced && !ord.IsFillExpected() {
		return true
	}

	// 2. 同値タッチかつ消化量達成判定
	isTouching := t.Price == ord.OrderPrice
	if isTouching && !ord.IsFillExpected() && ord.Synthetic.InitialQueueQty > 0 {
		threshold := ord.Synthetic.InitialQueueQty * p.QueueOffsetRatio
		return ord.Synthetic.ConsumedVolume >= threshold
	}

	return false
}

func (p *VolumeConsumptionPolicy) ShouldTimeout(ord *order.Order, t tick.Tick) bool {
	if !ord.IsFillExpected() {
		return false
	}
	elapsed := t.CurrentPriceTime.Sub(ord.Synthetic.ExpectedAt)
	return elapsed > 2*time.Second
}

func (p *VolumeConsumptionPolicy) ShouldReset(ord *order.Order, t tick.Tick) bool {
	return false
}

func (p *VolumeConsumptionPolicy) UpdateState(ord *order.Order, t tick.Tick) {
	if ord.OrderPrice <= 0 || t.Price <= 0 {
		return
	}

	// 1. 貫通および約定期待状態のときは更新しない
	isPierced := (ord.Action == order.ACTION_BUY && t.Price < ord.OrderPrice) ||
		(ord.Action == order.ACTION_SELL && t.Price > ord.OrderPrice)
	if isPierced || ord.IsFillExpected() {
		return
	}

	// 2. 同値タッチ時の状態初期化と出来高消化量更新
	isTouching := t.Price == ord.OrderPrice
	if isTouching {
		if ord.Synthetic.InitialQueueQty == 0 {
			if ord.Action == order.ACTION_BUY {
				ord.Synthetic.InitialQueueQty = t.BestBid.Qty
			} else {
				ord.Synthetic.InitialQueueQty = t.BestAsk.Qty
			}
			ord.Synthetic.LastVolumeUpdate = t.TradingVolume
			ord.Synthetic.ConsumedVolume = 0
			fmt.Printf("📝 [%s] 待ち行列の監視を開始: %s (Queue: %.0f)\n", ord.Symbol, ord.ID, ord.Synthetic.InitialQueueQty)
			return
		}

		if ord.Synthetic.LastVolumeUpdate > 0 && t.TradingVolume > ord.Synthetic.LastVolumeUpdate {
			deltaVol := t.TradingVolume - ord.Synthetic.LastVolumeUpdate
			ord.Synthetic.ConsumedVolume += deltaVol
		}
		ord.Synthetic.LastVolumeUpdate = t.TradingVolume
	} else {
		// 価格が離れている間も、総出来高の同期だけは維持する
		ord.Synthetic.LastVolumeUpdate = t.TradingVolume
	}
}

func (p *VolumeConsumptionPolicy) IsOrderDesired(ord *order.Order, sig brain.Signal, symbol symbol.Symbol) bool {
	return isOrderDesiredDefault(ord, sig, symbol)
}

// NoopPolicy は疑似約定判定を一切行いません（Observer戦略など向け）。
type NoopPolicy struct{}

func (p *NoopPolicy) ShouldFill(ord *order.Order, t tick.Tick) bool { return false }
func (p *NoopPolicy) ShouldTimeout(ord *order.Order, t tick.Tick) bool { return false }
func (p *NoopPolicy) ShouldReset(ord *order.Order, t tick.Tick) bool { return false }
func (p *NoopPolicy) UpdateState(ord *order.Order, t tick.Tick) {}

func (p *NoopPolicy) IsOrderDesired(ord *order.Order, sig brain.Signal, symbol symbol.Symbol) bool {
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
