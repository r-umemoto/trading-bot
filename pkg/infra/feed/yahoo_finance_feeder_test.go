package feed

import (
	"testing"

	"github.com/piquette/finance-go/datetime"
)

func TestNewYahooFinanceFeeder_SymbolFormatting(t *testing.T) {
	tests := []struct {
		name           string
		inputSymbol    string
		expectedSymbol string
	}{
		{
			name:           "Japanese stock code (4 digits)",
			inputSymbol:    "4005",
			expectedSymbol: "4005.T",
		},
		{
			name:           "US stock ticker",
			inputSymbol:    "AAPL",
			expectedSymbol: "AAPL",
		},
		{
			name:           "Japanese stock code already formatted",
			inputSymbol:    "4005.T",
			expectedSymbol: "4005.T",
		},
		{
			name:           "Non-numeric 4-character symbol",
			inputSymbol:    "A1B2",
			expectedSymbol: "A1B2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			feeder := NewYahooFinanceFeeder(tt.inputSymbol, datetime.OneDay)
			if feeder.symbol != tt.expectedSymbol {
				t.Errorf("expected symbol %q, got %q", tt.expectedSymbol, feeder.symbol)
			}
		})
	}
}
