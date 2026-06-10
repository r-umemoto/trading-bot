package calculator_test

import (
	"testing"

	"github.com/r-umemoto/trading-bot/pkg/domain/tick/calculator"
)

func TestSigmaCalculator_InitialState(t *testing.T) {
	calc := calculator.NewSigmaCalculator(1000.0)

	// Before any updates, activeVolume is 0, so Sigma should be 0, and VWAP should return the fallback price
	if sigma := calc.GetSigma(); sigma != 0.0 {
		t.Errorf("expected initial sigma to be 0, got %f", sigma)
	}

	if vwap := calc.GetVWAP(2000.0); vwap != 2000.0 {
		t.Errorf("expected initial VWAP to return fallback price 2000.0, got %f", vwap)
	}
}

func TestSigmaCalculator_Update_Normal(t *testing.T) {
	calc := calculator.NewSigmaCalculator(1000.0)

	// Tick 1: trading volume goes from 1000 to 1100 (+100 volume) at price 100.0
	calc.Update(1100.0, 100.0)

	if vwap := calc.GetVWAP(100.0); vwap != 100.0 {
		t.Errorf("expected VWAP after single tick to be 100.0, got %f", vwap)
	}

	if sigma := calc.GetSigma(); sigma != 0.0 {
		t.Errorf("expected sigma with single constant price to be 0.0 (no variance), got %f", sigma)
	}

	// Tick 2: trading volume goes from 1100 to 1200 (+100 volume) at price 200.0
	// Cumulative V = 200
	// Cumulative P*V = 100.0 * 100 + 200.0 * 100 = 30000
	// VWAP = 30000 / 200 = 150.0
	// Variance = (100^2 * 100 + 200^2 * 100) / 200 - 150^2
	//          = (1000000 + 4000000) / 200 - 22500
	//          = 25000 - 22500 = 2500
	// Sigma = sqrt(2500) = 50.0
	calc.Update(1200.0, 200.0)

	if vwap := calc.GetVWAP(200.0); vwap != 150.0 {
		t.Errorf("expected VWAP to be 150.0, got %f", vwap)
	}

	if sigma := calc.GetSigma(); sigma != 50.0 {
		t.Errorf("expected sigma to be 50.0, got %f", sigma)
	}
}

func TestSigmaCalculator_Update_NoNewVolume(t *testing.T) {
	calc := calculator.NewSigmaCalculator(1000.0)

	// If trading volume does not increase, Update should do nothing
	calc.Update(1000.0, 150.0) // same volume
	calc.Update(950.0, 150.0)  // decreased volume

	if sigma := calc.GetSigma(); sigma != 0.0 {
		t.Errorf("expected sigma to stay 0.0, got %f", sigma)
	}

	if vwap := calc.GetVWAP(200.0); vwap != 200.0 {
		t.Errorf("expected VWAP to return fallback since activeVolume is still 0, got %f", vwap)
	}
}

func TestSigmaCalculator_UpdateAndGetMetrics(t *testing.T) {
	calc := calculator.NewSigmaCalculator(1000.0)

	sigma, vwap := calc.UpdateAndGetMetrics(1200.0, 150.0)
	if sigma != 0.0 {
		t.Errorf("expected sigma to be 0.0, got %f", sigma)
	}
	if vwap != 150.0 {
		t.Errorf("expected vwap to be 150.0, got %f", vwap)
	}
}

func TestSigmaCalculator_NegativeVarianceClamping(t *testing.T) {
	// P1 = 10.0 and P2 = 10.0000001 triggers a negative variance in float64 due to precision loss:
	// variance = (10^2 + 10.0000001^2)/2 - (10 + 1e-7/2)^2 = -1.421085e-14
	calc := calculator.NewSigmaCalculator(0.0)
	calc.Update(1.0, 10.0)
	calc.Update(2.0, 10.0000001)

	sigma := calc.GetSigma()
	if sigma != 0.0 {
		t.Errorf("expected negative variance to be clamped to 0.0, got %f", sigma)
	}
}
