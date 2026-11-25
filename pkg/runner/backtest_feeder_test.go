package runner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
	"github.com/r-umemoto/trading-bot/pkg/portfolio"
)

func TestRunCustomCSVFeeder_ParsesAllFieldsAndUsesJST(t *testing.T) {
	tempDir := t.TempDir()
	csvContent := "Time,Symbol,Price,TradingVolume,VWAP,BestAskPrice,BestAskQty,BestBidPrice,BestBidQty," +
		"Ask2P,Ask2Q,Bid2P,Bid2Q,Ask3P,Ask3Q,Bid3P,Bid3Q,Ask4P,Ask4Q,Bid4P,Bid4Q," +
		"Ask5P,Ask5Q,Bid5P,Bid5Q,Ask6P,Ask6Q,Bid6P,Bid6Q,Ask7P,Ask7Q,Bid7P,Bid7Q," +
		"Ask8P,Ask8Q,Bid8P,Bid8Q,Ask9P,Ask9Q,Bid9P,Bid9Q,Ask10P,Ask10Q,Bid10P,Bid10Q," +
		"CurrentPriceStatus,CurrentPriceChangeStatus,OpeningPrice,PreviousClose,TradingValue," +
		"MarketOrderSellQty,MarketOrderBuyQty,OverSellQty,UnderBuyQty\n" +
		"09:05:12.345,5016,1945,12000,1944.8,1945.5,300,1944.5,400," +
		"1946,500,1944,600,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0," +
		"1,0057,1940,1820.5,23337600,456700,677600,837600,1604400\n"

	csvPath := filepath.Join(tempDir, "all_20261124.csv")
	if err := os.WriteFile(csvPath, []byte(csvContent), 0644); err != nil {
		t.Fatalf("failed to write CSV: %v", err)
	}

	ch := make(chan tick.Tick, 10)
	if err := runCustomCSVFeeder(csvPath, ch); err != nil {
		t.Fatalf("runCustomCSVFeeder failed: %v", err)
	}

	tk := <-ch
	if tk.Symbol != "5016" {
		t.Errorf("Symbol = %q, want 5016", tk.Symbol)
	}
	if tk.OpeningPrice != 1940 {
		t.Errorf("OpeningPrice = %v, want 1940", tk.OpeningPrice)
	}
	if tk.PreviousClose != 1820.5 {
		t.Errorf("PreviousClose = %v, want 1820.5", tk.PreviousClose)
	}
	if tk.TradingValue != 23337600 {
		t.Errorf("TradingValue = %v, want 23337600", tk.TradingValue)
	}
	if tk.MarketOrderSellQty != 456700 || tk.MarketOrderBuyQty != 677600 {
		t.Errorf("MarketOrderQty = (%v, %v), want (456700, 677600)", tk.MarketOrderSellQty, tk.MarketOrderBuyQty)
	}
	if tk.OverSellQty != 837600 || tk.UnderBuyQty != 1604400 {
		t.Errorf("Over/Under = (%v, %v), want (837600, 1604400)", tk.OverSellQty, tk.UnderBuyQty)
	}
	if tk.CurrentPriceStatus != tick.PriceStatus(1) {
		t.Errorf("CurrentPriceStatus = %v, want 1", tk.CurrentPriceStatus)
	}
	if tk.CurrentPriceChangeStatus != tick.PriceChangeStatus("0057") {
		t.Errorf("CurrentPriceChangeStatus = %q, want 0057", tk.CurrentPriceChangeStatus)
	}

	// Verify timestamp is fixed to JST (+09:00)
	_, offset := tk.CurrentPriceTime.Zone()
	if offset != 9*60*60 {
		t.Errorf("expected JST offset (+32400), got %d", offset)
	}
	if tk.CurrentPriceTime.Format("2006-01-02 15:04:05.000") != "2026-11-24 09:05:12.345" {
		t.Errorf("unexpected tick time: %v", tk.CurrentPriceTime)
	}
}

func TestInferPriceRangeGroupsFromCSV(t *testing.T) {
	tempDir := t.TempDir()
	csvContent := "Time,Symbol,Price,TradingVolume,VWAP,BestAskPrice,BestAskQty,BestBidPrice,BestBidQty,CurrentPriceStatus\n" +
		"09:00:00.000,5016,1944.5,1000,1944.5,1945.0,100,1944.5,100,1\n" +
		"09:00:00.000,7203,2500.0,1000,2500.0,2501.0,100,2500.0,100,1\n"

	csvPath := filepath.Join(tempDir, "all_20261124.csv")
	if err := os.WriteFile(csvPath, []byte(csvContent), 0644); err != nil {
		t.Fatalf("failed to write CSV: %v", err)
	}

	assets := map[string]portfolio.SymbolTarget{
		"5016": {Symbol: "5016", Enabled: true},
		"7203": {Symbol: "7203", Enabled: true},
	}

	inferred := inferPriceRangeGroupsFromCSV(csvPath, assets)
	if inferred["5016"] != symbol.PRICE_RANGE_GROUP_TSE_TOPIX100 {
		t.Errorf("expected 5016 to be inferred as TOPIX100, got %v", inferred["5016"])
	}
	if inferred["7203"] != symbol.PRICE_RANGE_GROUP_TSE_STANDARD {
		t.Errorf("expected 7203 to be inferred as TSE_STANDARD, got %v", inferred["7203"])
	}
}
