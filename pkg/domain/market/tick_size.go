package market

import "math"

// PriceRangeGroup はカブコムAPIから返される呼値グループ識別子です
type PriceRangeGroup string

const (
	PRICE_RANGE_GROUP_TSE_STANDARD PriceRangeGroup = "10000" // 東証標準
	PRICE_RANGE_GROUP_TSE_TOPIX100 PriceRangeGroup = "10003" // TOPIX100構成銘柄
)

// CalcTickSize は価格に応じた最小呼値（ティックサイズ）を計算します
func (d *SymbolDetail) CalcTickSize(price float64) float64 {
	absPrice := math.Abs(price)

	switch d.PriceRangeGroup {
	case PRICE_RANGE_GROUP_TSE_TOPIX100:
		return getTSETopix100TickSize(absPrice)
	case PRICE_RANGE_GROUP_TSE_STANDARD:
		fallthrough
	default:
		return getTSEStandardTickSize(absPrice)
	}
}

// getTSEStandardTickSize は東証標準（グループ1）の呼値を返します
func getTSEStandardTickSize(price float64) float64 {
	switch {
	case price <= 1000:
		return 1.0
	case price <= 3000:
		return 1.0
	case price <= 5000:
		return 5.0
	case price <= 10000:
		return 10.0
	case price <= 30000:
		return 10.0
	case price <= 50000:
		return 50.0
	case price <= 100000:
		return 100.0
	case price <= 300000:
		return 100.0
	case price <= 500000:
		return 500.0
	case price <= 1000000:
		return 1000.0
	case price <= 3000000:
		return 1000.0
	case price <= 5000000:
		return 5000.0
	default:
		return 10000.0
	}
}

// getTSETopix100TickSize はTOPIX100銘柄の呼値を返します（0.1円刻みなどがある）
func getTSETopix100TickSize(price float64) float64 {
	switch {
	case price <= 1000:
		return 0.1
	case price <= 3000:
		return 0.5
	case price <= 5000:
		return 1.0
	case price <= 10000:
		return 1.0
	case price <= 30000:
		return 5.0
	case price <= 50000:
		return 10.0
	case price <= 100000:
		return 10.0
	case price <= 300000:
		return 50.0
	case price <= 500000:
		return 100.0
	case price <= 1000000:
		return 100.0
	case price <= 3000000:
		return 500.0
	default:
		return 1000.0
	}
}
