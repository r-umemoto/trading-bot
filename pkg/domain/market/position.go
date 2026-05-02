package market

// Position は保有している建玉（または現物）の状態を表すエンティティです
type Position struct {
	ExecutionID string
	Symbol      string // 銘柄
	Exchange    ExchangeMarket
	Action      Action
	TradeType   MarginTradeType
	AccountType AccountType
	LeavesQty   float64 // 保有数量
	Price       float64 // 取得価格
}
