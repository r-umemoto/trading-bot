package strategy_test

import (
	"testing"

	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
)

func TestStrategyRegistry(t *testing.T) {
	// Test successful strategy factory lookup
	factory, err := strategy.GetFactory("sample")
	if err != nil {
		t.Fatalf("expected sample strategy factory to be found, got error: %v", err)
	}
	if factory == nil {
		t.Fatal("expected strategy factory not to be nil")
	}

	// Test strategy lookup failure
	_, err = strategy.GetFactory("non_existent_strategy")
	if err == nil {
		t.Error("expected error for non-existent strategy, got nil")
	}
}
