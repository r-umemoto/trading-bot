package sniper

import (
	"testing"

	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
)

func TestCommittedPosition(t *testing.T) {
	longPos := CommittedPosition{TotalQty: 100}
	if !longPos.IsLong() || longPos.IsShort() || longPos.IsFlat() {
		t.Errorf("expected long position, got %+v", longPos)
	}
	if longPos.AbsQty() != 100 {
		t.Errorf("expected AbsQty 100, got %f", longPos.AbsQty())
	}

	shortPos := CommittedPosition{TotalQty: -100}
	if !shortPos.IsShort() || shortPos.IsLong() || shortPos.IsFlat() {
		t.Errorf("expected short position, got %+v", shortPos)
	}
	if shortPos.AbsQty() != 100 {
		t.Errorf("expected AbsQty 100, got %f", shortPos.AbsQty())
	}

	flatPos := CommittedPosition{TotalQty: 0}
	if !flatPos.IsFlat() || flatPos.IsLong() || flatPos.IsShort() {
		t.Errorf("expected flat position, got %+v", flatPos)
	}
	if flatPos.AbsQty() != 0 {
		t.Errorf("expected AbsQty 0, got %f", flatPos.AbsQty())
	}

	// 振る舞いテスト: RequiresReversal
	targetShort := strategy.TargetPosition{Qty: -50}
	if !longPos.RequiresReversal(targetShort) {
		t.Errorf("expected long position to require reversal when target is short")
	}
	targetLong := strategy.TargetPosition{Qty: 50}
	if !shortPos.RequiresReversal(targetLong) {
		t.Errorf("expected short position to require reversal when target is long")
	}
	if flatPos.RequiresReversal(targetLong) || flatPos.RequiresReversal(targetShort) {
		t.Errorf("expected flat position not to require reversal")
	}

	// 振る舞いテスト: GapTo
	// 1. Long 100 -> Target 150 => Gap: 50
	if gap := longPos.GapTo(strategy.TargetPosition{Qty: 150}); gap != 50 {
		t.Errorf("expected gap 50, got %f", gap)
	}
	// 2. Long 100 -> Target 0 => Gap: 100
	if gap := longPos.GapTo(strategy.TargetPosition{Qty: 0}); gap != 100 {
		t.Errorf("expected gap 100, got %f", gap)
	}
	// 3. Short 100 -> Target -100 => Gap: 0
	if gap := shortPos.GapTo(strategy.TargetPosition{Qty: -100}); gap != 0 {
		t.Errorf("expected gap 0, got %f", gap)
	}
}
