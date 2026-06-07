package sniper

import (
	"testing"

	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
)

func TestDefaultOperation(t *testing.T) {
	sym := symbol.Symbol{Code: "7203", Name: "Toyota"}
	nest := NewSniperNest("7203", sym, nil, nil)

	op := NewDefaultOperation("op-1", nest)

	// 1. GetID
	if op.GetID() != "op-1" {
		t.Errorf("expected GetID 'op-1', got '%s'", op.GetID())
	}

	// 2. GetSymbolCodes
	codes := op.GetSymbolCodes()
	if len(codes) != 1 || codes[0] != "7203" {
		t.Errorf("unexpected GetSymbolCodes: %v", codes)
	}

	// 3. Embedded SniperNest delegation
	if op.GetSymbolCode() != "7203" {
		t.Errorf("expected GetSymbolCode '7203', got '%s'", op.GetSymbolCode())
	}
}
