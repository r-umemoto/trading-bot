package sniper_test

import (
	"testing"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
)

func TestCooldownTracker(t *testing.T) {
	ct := sniper.NewCooldownTracker()
	sniperID := "test-sniper-1"
	now := time.Now()

	// 1. Initially, no cooldown should exist
	if ct.IsCoolingDown(sniperID, now) {
		t.Error("expected no cooldown initially")
	}

	// 2. Trigger with TriggerWithTime (100ms ago) -> should be cooling down
	ct.TriggerWithTime(sniperID, now.Add(-100*time.Millisecond))
	if !ct.IsCoolingDown(sniperID, now) {
		t.Error("expected cooling down when last close error was 100ms ago")
	}

	// 3. 1.1 seconds later -> should NOT be cooling down
	if ct.IsCoolingDown(sniperID, now.Add(1100*time.Millisecond)) {
		t.Error("expected cooldown to expire after 1.1 seconds")
	}

	// 4. Trigger (uses time.Now()) -> should be cooling down immediately
	ct.Trigger(sniperID)
	if !ct.IsCoolingDown(sniperID, time.Now()) {
		t.Error("expected cooling down immediately after Trigger")
	}
}
