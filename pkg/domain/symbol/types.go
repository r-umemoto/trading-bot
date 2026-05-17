package symbol

type PriceRangeGroup int

const (
	PRICE_RANGE_GROUP_TSE_STANDARD PriceRangeGroup = 10000 // 東証標準
	PRICE_RANGE_GROUP_TSE_TOPIX100 PriceRangeGroup = 10003 // TOPIX100構成銘柄
)
