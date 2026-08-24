package position

import (
	"fmt"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
)

// PositionMeta は建玉に付随する分析・ロギング用のメタデータです
type PositionMeta struct {
	EntryTime time.Time // 🌟 約定時刻
}

// Position は保有している建玉（または現物）の状態を表すエンティティです
type Position struct {
	ExecutionID string
	Symbol      string // 銘柄
	Exchange    order.ExchangeMarket
	Action      order.Action
	TradeType   order.MarginTradeType
	AccountType order.AccountType
	LeavesQty   float64            // 保有数量
	Locks       map[string]float64 // 🌟 新設: Key: OrderID -> Value: ロック中の数量
	Price       float64            // 取得価格
	Meta        PositionMeta       // 🌟 分析用メタデータ
}

// AvailableQty はロックされていない、発注可能な余力数量を返します
func (p *Position) AvailableQty() float64 {
	locked := 0.0
	for _, qty := range p.Locks {
		locked += qty
	}
	return p.LeavesQty - locked
}

// Lock は指定された注文IDによって数量をロック（予約）します
func (p *Position) Lock(orderID string, qty float64) error {
	available := p.AvailableQty()
	if qty > available {
		return fmt.Errorf("insufficient available quantity: want %f, available %f", qty, available)
	}
	if p.Locks == nil {
		p.Locks = make(map[string]float64)
	}
	p.Locks[orderID] += qty
	return nil
}

// Unlock は指定された注文IDに紐づくロックを解除します
func (p *Position) Unlock(orderID string) {
	if p.Locks != nil {
		delete(p.Locks, orderID)
	}
}
