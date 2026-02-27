package calculator

import (
	"math"
)

// 銘柄ごとの標準偏差を計算・保持する構造体
type SigmaCalculator struct {
	prevVolume float64 // 前回のAPI累積売買高（差分計算用）

	// 以下の3つは「システム起動後」からの純粋な累積値
	activeVolume    float64 // 起動後からの累積出来高 (ΣV)
	sumPriceVolume  float64 // 起動後からの Σ(P * V)
	sumPrice2Volume float64 // 起動後からの Σ(P^2 * V)
}

// NewSigmaCalculator は初期出来高をセットして構造体を初期化します
func NewSigmaCalculator(initialVolume float64) *SigmaCalculator {
	return &SigmaCalculator{
		prevVolume: initialVolume,
	}
}

// PUSH通知を受信するたびに呼び出してσを取得する関数
// （引数の vwap はAPIから取得した当日VWAPですが、分散計算には内部計算のVWAPを使います）
func (s *SigmaCalculator) UpdateAndGetSigma(tradingVolume float64, currentPrice float64) float64 {
	// 1. 今回の通信での差分出来高（Tick Volume）を算出
	tickVolume := tradingVolume - s.prevVolume

	// 気配値の更新のみで約定が発生していない（出来高増減なし）場合は前回の計算結果を返す
	if tickVolume <= 0 {
		return s.calculateSigma()
	}

	// 2. 約定が発生している場合、起動後からの各種累積値を更新
	s.activeVolume += tickVolume
	s.sumPriceVolume += currentPrice * tickVolume
	s.sumPrice2Volume += (currentPrice * currentPrice) * tickVolume
	s.prevVolume = tradingVolume

	// 3. 標準偏差（σ）を計算して返す
	return s.calculateSigma()
}

// 内部計算用メソッド
func (s *SigmaCalculator) calculateSigma() float64 {
	// 起動直後でまだ約定がない場合は0
	if s.activeVolume == 0 {
		return 0
	}

	// 起動後からのデータに基づくローカルなVWAPを計算
	localVWAP := s.sumPriceVolume / s.activeVolume

	// 分散 = (Σ(P^2 * V) / 起動後総出来高) - ローカルVWAP^2
	variance := (s.sumPrice2Volume / s.activeVolume) - (localVWAP * localVWAP)

	// 浮動小数点計算の誤差によるマイナス値を防止
	if variance < 0 {
		variance = 0
	}

	return math.Sqrt(variance)
}
