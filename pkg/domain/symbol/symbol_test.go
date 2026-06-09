package symbol_test

import (
	"testing"

	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
)

func TestSymbol_String(t *testing.T) {
	s := symbol.Symbol{
		Code: "7203",
		Name: "トヨタ自動車",
	}
	expected := "トヨタ自動車 (7203)"
	if s.String() != expected {
		t.Errorf("expected %q, got %q", expected, s.String())
	}
}

func TestSymbol_CalcTickSize_Standard(t *testing.T) {
	s := symbol.Symbol{
		Code:            "7203",
		Name:            "トヨタ",
		PriceRangeGroup: symbol.PRICE_RANGE_GROUP_TSE_STANDARD,
	}

	tests := []struct {
		price    float64
		expected float64
	}{
		{500, 1.0},
		{1000, 1.0},
		{1001, 1.0},
		{3000, 1.0},
		{3005, 5.0},
		{5000, 5.0},
		{5010, 10.0},
		{10000, 10.0},
		{10010, 10.0},
		{30000, 10.0},
		{30050, 50.0},
		{50000, 50.0},
		{50100, 100.0},
		{100000, 100.0},
		{100100, 100.0},
		{300000, 100.0},
		{300500, 500.0},
		{500000, 500.0},
		{501000, 1000.0},
		{1000000, 1000.0},
		{1001000, 1000.0},
		{3000000, 1000.0},
		{3005000, 5000.0},
		{5000000, 5000.0},
		{5010000, 10000.0},
		{10000000, 10000.0},
		// Negative price checks
		{-1000, 1.0},
		{-5000000, 5000.0},
	}

	for _, tc := range tests {
		actual := s.CalcTickSize(tc.price)
		if actual != tc.expected {
			t.Errorf("Standard Group: price %f: expected tick %f, got %f", tc.price, tc.expected, actual)
		}
	}
}

func TestSymbol_CalcTickSize_Topix100(t *testing.T) {
	s := symbol.Symbol{
		Code:            "7203",
		Name:            "トヨタ",
		PriceRangeGroup: symbol.PRICE_RANGE_GROUP_TSE_TOPIX100,
	}

	tests := []struct {
		price    float64
		expected float64
	}{
		{500, 0.1},
		{1000, 0.1},
		{1000.5, 0.5},
		{3000, 0.5},
		{3001, 1.0},
		{5000, 1.0},
		{5001, 1.0},
		{10000, 1.0},
		{10005, 5.0},
		{30000, 5.0},
		{30010, 10.0},
		{50000, 10.0},
		{50010, 10.0},
		{100000, 10.0},
		{100050, 50.0},
		{300000, 50.0},
		{300100, 100.0},
		{500000, 100.0},
		{500100, 100.0},
		{1000000, 100.0},
		{1000500, 500.0},
		{3000000, 500.0},
		{3001000, 1000.0},
		{5000000, 1000.0},
		// Negative price checks
		{-500, 0.1},
		{-3000000, 500.0},
	}

	for _, tc := range tests {
		actual := s.CalcTickSize(tc.price)
		if actual != tc.expected {
			t.Errorf("Topix100 Group: price %f: expected tick %f, got %f", tc.price, tc.expected, actual)
		}
	}
}

func TestSymbol_RoundPrice(t *testing.T) {
	standardSymbol := symbol.Symbol{
		Code:            "7203",
		Name:            "トヨタ",
		PriceRangeGroup: symbol.PRICE_RANGE_GROUP_TSE_STANDARD,
	}

	topixSymbol := symbol.Symbol{
		Code:            "7203",
		Name:            "トヨタ",
		PriceRangeGroup: symbol.PRICE_RANGE_GROUP_TSE_TOPIX100,
	}

	// 1. Standard rounding
	standardTests := []struct {
		input    float64
		expected float64
	}{
		{1500.4, 1500.0},
		{1500.6, 1501.0},
		{4123.0, 4125.0}, // tick is 5.0 at this range
		{4122.4, 4120.0},
		{4122.5, 4125.0},
		{15423.0, 15420.0}, // tick is 10.0
		{15425.0, 15430.0},
	}

	for _, tc := range standardTests {
		actual := standardSymbol.RoundPrice(tc.input)
		if actual != tc.expected {
			t.Errorf("Standard: input %f: expected rounded %f, got %f", tc.input, tc.expected, actual)
		}
	}

	// 2. Topix100 rounding & IEEE 754 precision cleanup
	topixTests := []struct {
		input    float64
		expected float64
	}{
		{418.91, 418.9},
		{418.94, 418.9},
		{418.95, 419.0},
		{1000.2, 1000.0}, // tick is 0.5 above 1000
		{1000.25, 1000.5},
		{1000.24, 1000.0},
	}

	for _, tc := range topixTests {
		actual := topixSymbol.RoundPrice(tc.input)
		if actual != tc.expected {
			t.Errorf("Topix100: input %f: expected rounded %f, got %f", tc.input, tc.expected, actual)
		}
	}

	// 3. Fallback for zero or invalid tick size (should return input directly)
	invalidSymbol := symbol.Symbol{
		Code:            "9999",
		Name:            "ダミー",
		PriceRangeGroup: 999, // Unrecognized price group
	}

	if invalidSymbol.CalcTickSize(100.0) != 0.0 {
		t.Errorf("expected 0.0 tick size for invalid price range group, got %f", invalidSymbol.CalcTickSize(100.0))
	}

	roundedPrice := invalidSymbol.RoundPrice(1234.56)
	if roundedPrice != 1234.56 {
		t.Errorf("expected price to be returned completely untouched when tick is 0, got %f", roundedPrice)
	}
}
