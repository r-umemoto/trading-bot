package calculator

import (
	"math"
)

// 銘柄ごとの標準偏差を計算・保持する構造体
type SigmaCalculator struct {
	prevVolume      float64 // 前回の累積売買高
	sumPrice2Volume float64 // Σ(P^2 * V) の累積
}

// PUSH通知を受信するたびに呼び出してσを取得する関数
func (s *SigmaCalculator) UpdateAndGetSigma(tradingVolume float64, vwap float64, currentPrice float64) float64 {
	// 1. 今回の通信での差分出来高（Tick Volume）を算出
	tickVolume := tradingVolume - s.prevVolume

	// 気配値の更新のみで約定が発生していない（出来高増減なし）場合は計算をスキップ
	if tickVolume <= 0 {
		return s.calculateSigma(tradingVolume, vwap)
	}

	// 2. 約定が発生している場合、Σ(P^2 * V)を更新
	s.sumPrice2Volume += (currentPrice * currentPrice) * tickVolume
	s.prevVolume = tradingVolume

	// 3. 標準偏差（σ）を計算して返す
	return s.calculateSigma(tradingVolume, vwap)
}

// 内部計算用メソッド
func (s *SigmaCalculator) calculateSigma(totalVolume float64, vwap float64) float64 {
	if totalVolume == 0 {
		return 0
	}

	// 分散 = (Σ(P^2 * V) / 総出来高) - VWAP^2
	variance := (s.sumPrice2Volume / totalVolume) - (vwap * vwap)

	// 浮動小数点計算の誤差によるマイナス値を防止
	if variance < 0 {
		variance = 0
	}

	return math.Sqrt(variance)
}
