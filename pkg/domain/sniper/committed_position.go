package sniper

import (
	"math"

	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
)

// CommittedPosition は、市場に対して既にコミットしている執行中・約定済みポジションの総計を表す値オブジェクト（VO）です。
// 取引所で約定した物理建玉と、未約定のアクティブ注文（成行・指値・返済問わずすべて）を純合算した状態を保持します。
// 戦略ドメインの strategy.Position（損益評価用建玉）とは明確に分離された、執行調停ドメイン専用の豊かなVOです。
type CommittedPosition struct {
	TotalQty float64 // 純総コミット数量（買いはプラス、売りはマイナス）
}

// AbsQty は純総コミット数量の絶対値を返します
func (cp CommittedPosition) AbsQty() float64 {
	return math.Abs(cp.TotalQty)
}

// IsLong は買いコミット状態かどうかを返します
func (cp CommittedPosition) IsLong() bool {
	return cp.TotalQty > 0
}

// IsShort は売りコミット状態かどうかを返します
func (cp CommittedPosition) IsShort() bool {
	return cp.TotalQty < 0
}

// IsFlat はコミット枠がゼロ（ノーポジかつ注文なし）かどうかを返します
func (cp CommittedPosition) IsFlat() bool {
	return cp.TotalQty == 0
}

// RequiresReversal は、指定された目標ポジションに対してドテン（ポジション反転）が必要かどうかを判定します
func (cp CommittedPosition) RequiresReversal(target strategy.TargetPosition) bool {
	return (cp.IsLong() && target.IsShort()) || (cp.IsShort() && target.IsLong())
}

// GapTo は目標ポジションに対して不足している数量（ギャップの絶対値）を計算して返します
func (cp CommittedPosition) GapTo(target strategy.TargetPosition) float64 {
	return math.Abs(target.Qty - cp.TotalQty)
}

// DeltaTo は現在のコミット数量から目標ポジションへ到達するために必要な売買数量（差分）を返します。
// 正の値は買い（新規買いまたはショート返済買い）、負の値は売り（新規売りまたはロング返済売り）を意味します。
func (cp CommittedPosition) DeltaTo(target strategy.TargetPosition) float64 {
	return target.Qty - cp.TotalQty
}
