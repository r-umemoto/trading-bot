package symbol

import (
	"fmt"
	"math"
	"strconv"
)

// CalcTickSize は価格に応じた最小呼値（ティックサイズ）を計算します
func (d *Symbol) CalcTickSize(price float64) float64 {
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

// RoundPrice は指定された価格を、その銘柄の現在の呼値（ティックサイズ）の倍数に最も近い値に丸めます
func (d *Symbol) RoundPrice(price float64) float64 {
	tick := d.CalcTickSize(price)
	if tick <= 0 {
		return price
	}
	// math.Round を使って最も近い呼び値の倍数にする
	rounded := math.Round(price/tick) * tick
	
	// IEEE754浮動小数点の演算誤差（例: 418.90000000000003）を消去するため、
	// 小数点以下1桁の文字列表現を経由してクリーンなfloat64を生成する
	cleanPrice, _ := strconv.ParseFloat(fmt.Sprintf("%.1f", rounded), 64)
	return cleanPrice
}
