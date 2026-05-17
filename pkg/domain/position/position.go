package position

import (
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
	LeavesQty   float64      // 保有数量
	Price       float64      // 取得価格
	Meta        PositionMeta // 🌟 分析用メタデータ
}
