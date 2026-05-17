package tick

// PriceStatus は現値のステータスを表す抽象的な型です
type PriceStatus int

const (
	PRICE_STATUS_NONE      PriceStatus = 0
	PRICE_STATUS_CURRENT   PriceStatus = 1 // 現値
	PRICE_STATUS_OPENING   PriceStatus = 2 // 寄付
	PRICE_STATUS_PRE_CLOSE PriceStatus = 3 // 前引
	PRICE_STATUS_CLOSE     PriceStatus = 4 // 大引
	PRICE_STATUS_SPECIAL   PriceStatus = 5 // 特別気配
)

// PriceChangeStatus は前値比較のステータスを表します
type PriceChangeStatus string

const (
	PRICE_CHANGE_NONE      PriceChangeStatus = ""
	PRICE_CHANGE_UP        PriceChangeStatus = "UP"
	PRICE_CHANGE_DOWN      PriceChangeStatus = "DOWN"
	PRICE_CHANGE_UNCHANGED PriceChangeStatus = "UNCHANGED"
)
