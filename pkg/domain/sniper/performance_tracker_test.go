package sniper_test

import (
	"testing"

	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
)

func TestPerformanceTracker_Get_NewSniper(t *testing.T) {
	pet := sniper.NewPerformanceTracker()
	sniperID := "new-sniper-id"

	// Querying a non-existent sniper should return a zeroed Performance struct without crashing.
	perf := pet.Get(sniperID)
	if perf.Trades != 0 || perf.Wins != 0 || perf.Losses != 0 || perf.RealizedPnL != 0.0 || perf.UnrealizedPnL != 0.0 {
		t.Errorf("expected zeroed Performance for non-existent sniper, got %+v", perf)
	}
}

func TestPerformanceTracker_RecordPnL_WinsLossesAndZero(t *testing.T) {
	pet := sniper.NewPerformanceTracker()
	sniperID := "test-sniper-id"

	// 1. Record a Win (PnL > 0)
	pet.RecordPnL(sniperID, 1500.50)
	perf := pet.Get(sniperID)
	if perf.Trades != 1 {
		t.Errorf("expected 1 trade, got %d", perf.Trades)
	}
	if perf.Wins != 1 {
		t.Errorf("expected 1 win, got %d", perf.Wins)
	}
	if perf.Losses != 0 {
		t.Errorf("expected 0 losses, got %d", perf.Losses)
	}
	if perf.RealizedPnL != 1500.50 {
		t.Errorf("expected RealizedPnL to be 1500.50, got %f", perf.RealizedPnL)
	}

	// 2. Record a Loss (PnL < 0)
	pet.RecordPnL(sniperID, -500.25)
	perf = pet.Get(sniperID)
	if perf.Trades != 2 {
		t.Errorf("expected 2 trades, got %d", perf.Trades)
	}
	if perf.Wins != 1 {
		t.Errorf("expected 1 win, got %d", perf.Wins)
	}
	if perf.Losses != 1 {
		t.Errorf("expected 1 loss, got %d", perf.Losses)
	}
	if perf.RealizedPnL != 1000.25 {
		t.Errorf("expected RealizedPnL to be 1000.25 (1500.50 - 500.25), got %f", perf.RealizedPnL)
	}

	// 3. Record a Flat Trade (PnL == 0) - Should increment Trades but neither Wins nor Losses
	pet.RecordPnL(sniperID, 0.0)
	perf = pet.Get(sniperID)
	if perf.Trades != 3 {
		t.Errorf("expected 3 trades, got %d", perf.Trades)
	}
	if perf.Wins != 1 {
		t.Errorf("expected 1 win, got %d", perf.Wins)
	}
	if perf.Losses != 1 {
		t.Errorf("expected 1 loss, got %d", perf.Losses)
	}
	if perf.RealizedPnL != 1000.25 {
		t.Errorf("expected RealizedPnL to stay 1000.25, got %f", perf.RealizedPnL)
	}
}

func TestPerformanceTracker_MultipleSnipers(t *testing.T) {
	pet := sniper.NewPerformanceTracker()
	id1 := "sniper-1"
	id2 := "sniper-2"

	// Record wins for sniper-1, losses for sniper-2
	pet.RecordPnL(id1, 100)
	pet.RecordPnL(id2, -50)

	perf1 := pet.Get(id1)
	perf2 := pet.Get(id2)

	if perf1.Wins != 1 || perf1.RealizedPnL != 100.0 {
		t.Errorf("unexpected metrics for sniper-1: %+v", perf1)
	}
	if perf2.Losses != 1 || perf2.RealizedPnL != -50.0 {
		t.Errorf("unexpected metrics for sniper-2: %+v", perf2)
	}
}
