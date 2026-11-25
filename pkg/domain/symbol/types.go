package symbol

type PriceRangeGroup int

const (
	PRICE_RANGE_GROUP_TSE_STANDARD     PriceRangeGroup = 10000 // 東証標準
	PRICE_RANGE_GROUP_TSE_STANDARD_ALT PriceRangeGroup = 10001 // 東証標準（REIT等）
	PRICE_RANGE_GROUP_TSE_TOPIX100     PriceRangeGroup = 10003 // TOPIX100構成銘柄
	PRICE_RANGE_GROUP_TSE_ETF          PriceRangeGroup = 10118 // 東証ETF/ETN（細分呼値）
	PRICE_RANGE_GROUP_TSE_ETF_FINE     PriceRangeGroup = 10119 // 東証レバレッジETF等（細分呼値）
)
