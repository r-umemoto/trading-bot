package calculator

import (
	"testing"
	"time"
)

func TestFiveMinCalculator_Update(t *testing.T) {
	calc := NewFiveMinCalculator()
	
	// 10:00:00 のデータ
	baseTime := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)
	
	// 1. 最初のアライメント
	calc.Update(100.0, 1000.0, baseTime)
	if calc.windowStart != baseTime {
		t.Errorf("expected windowStart %v, got %v", baseTime, calc.windowStart)
	}
	if calc.windowStartVol != 1000.0 {
		t.Errorf("expected windowStartVol 1000, got %f", calc.windowStartVol)
	}

	// 2. 同じ枠内での追加
	calc.Update(110.0, 1100.0, baseTime.Add(1*time.Minute))
	calc.Update(120.0, 1200.0, baseTime.Add(2*time.Minute))
	
	if len(calc.ticks) != 3 {
		t.Errorf("expected 3 ticks, got %d", len(calc.ticks))
	}

	// 3. 5分境界をまたぐ (10:05:00)
	nextBaseTime := baseTime.Add(5 * time.Minute)
	// 10:05:01 のデータ
	calc.Update(130.0, 1300.0, nextBaseTime.Add(1*time.Second))
	
	// summaries が1つ作成されているはず
	summaries := calc.GetSummaries()
	if len(summaries) != 1 {
		t.Errorf("expected 1 summary, got %d", len(summaries))
	}
	
	// 集計結果の確認 (10:00:00〜10:05:00)
	// 110.0 * (1100-1000) + 120.0 * (1200-1100) = 11000 + 12000 = 23000
	// 10:05:01のデータは次のバーなので含まれない
	// volume = 1200 - 1000 = 200
	// vwap = 23000 / 200 = 115.0
	expectedVWAP := 115.0
	if summaries[0].VWAP != expectedVWAP {
		t.Errorf("expected summary VWAP %f, got %f", expectedVWAP, summaries[0].VWAP)
	}
	
	// 新しい枠の状態確認
	if calc.windowStart != nextBaseTime {
		t.Errorf("expected windowStart %v, got %v", nextBaseTime, calc.windowStart)
	}
	
	// ticks にはスライディングVWAPのために、前の枠のデータも1つ残っているはず
	// (10:05:01時点では、10:00:00がベースラインとして残っているべき)
	if len(calc.ticks) <= 1 {
		t.Errorf("expected more ticks for sliding window, got %d", len(calc.ticks))
	}

	// GetCurrentVWAP がスライディングになっているか確認
	// 10:05:01時点での直近5分間(10:00:01〜10:05:01)のデータ
	// 現在のティック:
	// 10:00:00 (100.0, 1000.0) -> ベースライン
	// 10:01:00 (110.0, 1100.0) -> vol: 1100-1000 = 100
	// 10:02:00 (120.0, 1200.0) -> vol: 1200-1100 = 100
	// 10:05:01 (130.0, 1300.0) -> vol: 1300-1200 = 100
	// vwap = (110*100 + 120*100 + 130*100) / 300 = 36000 / 300 = 120.0
	slidingVWAP := calc.GetCurrentVWAP()
	expectedSlidingVWAP := 120.0
	if slidingVWAP != expectedSlidingVWAP {
		t.Errorf("expected sliding VWAP %f, got %f", expectedSlidingVWAP, slidingVWAP)
	}
}

func TestFiveMinCalculator_GetCurrentVWAP_EdgeCases(t *testing.T) {
	calc := NewFiveMinCalculator()
	baseTime := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	// 1. 最初
	if vwap := calc.GetCurrentVWAP(); vwap != 0 {
		t.Errorf("expected 0, got %f", vwap)
	}

	// 2. 1つ目のデータ（増分不明なので直近価格を返す）
	calc.Update(100.0, 1000.0, baseTime)
	if vwap := calc.GetCurrentVWAP(); vwap != 100.0 {
		t.Errorf("expected 100.0, got %f", vwap)
	}

	// 3. ウィンドウ外に押し出されるケース
	calc.Update(110.0, 1100.0, baseTime.Add(1*time.Minute))
	calc.Update(120.0, 1200.0, baseTime.Add(2*time.Minute))
	calc.Update(130.0, 1300.0, baseTime.Add(7*time.Minute)) // 10:07:00, slidingStart = 10:02:00

	// 10:02:00 (120.0, 1200.0) は time >= slidingStart なのでウィンドウに含まれる
	// 10:01:00 (110.0, 1100.0) がベースラインになる
	// 10:02:00 (120.0, 1200.0) -> vol: 1200-1100 = 100
	// 10:07:00 (130.0, 1300.0) -> vol: 1300-1200 = 100
	// VWAP = (120*100 + 130*100) / 200 = 125.0
	if vwap := calc.GetCurrentVWAP(); vwap != 125.0 {
		t.Errorf("expected 125.0, got %f", vwap)
	}
}
