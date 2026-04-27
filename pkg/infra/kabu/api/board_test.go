package api

import (
	"encoding/json"
	"testing"
	"time"
)

func TestBoardResponse_UnmarshalJSON(t *testing.T) {
	sampleJSON := `{
		"Symbol": "5401",
		"SymbolName": "日本製鉄",
		"Exchange": 1,
		"ExchangeName": "東証プライム",
		"CurrentPrice": 2408.0,
		"CurrentPriceTime": "2022-04-04T15:00:00+09:00",
		"CurrentPriceChangeStatus": "0058",
		"CurrentPriceStatus": 1,
		"CalcPrice": 2408.0,
		"PreviousClose": 2400.0,
		"PreviousCloseTime": "2022-04-01T00:00:00+09:00",
		"ChangePreviousClose": 8.0,
		"ChangePreviousClosePer": 0.33,
		"OpeningPrice": 2380.0,
		"OpeningPriceTime": "2022-04-04T09:00:00+09:00",
		"HighPrice": 2418.0,
		"HighPriceTime": "2022-04-04T10:00:00+09:00",
		"LowPrice": 2370.0,
		"LowPriceTime": "2022-04-04T09:30:00+09:00",
		"TradingVolume": 1000000.0,
		"TradingVolumeTime": "2022-04-04T15:00:00+09:00",
		"VWAP": 2395.5,
		"TradingValue": 2395500000.0,
		"BidQty": 1000.0,
		"BidPrice": 2407.0,
		"BidTime": "2022-04-04T15:00:00+09:00",
		"BidSign": "0101",
		"AskQty": 1500.0,
		"AskPrice": 2409.0,
		"AskTime": "2022-04-04T15:00:00+09:00",
		"AskSign": "0101",
		"Sell1": {
			"Price": 2409.0,
			"Qty": 1500.0,
			"Time": "2022-04-04T15:00:00+09:00",
			"Sign": "0101"
		},
		"Sell2": {
			"Price": 2410.0,
			"Qty": 2000.0
		},
		"Buy1": {
			"Price": 2407.0,
			"Qty": 1000.0,
			"Time": "2022-04-04T15:00:00+09:00",
			"Sign": "0101"
		},
		"Buy2": {
			"Price": 2406.0,
			"Qty": 3000.0
		},
		"MarketOrderSellQty": 0.0,
		"MarketOrderBuyQty": 0.0,
		"OverSellQty": 50000.0,
		"UnderBuyQty": 60000.0,
		"TotalMarketValue": 2200000000000.0,
		"SecurityType": 1
	}`

	var resp BoardResponse
	err := json.Unmarshal([]byte(sampleJSON), &resp)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if resp.Symbol != "5401" {
		t.Errorf("Expected Symbol 5401, got %s", resp.Symbol)
	}
	if resp.SymbolName != "日本製鉄" {
		t.Errorf("Expected SymbolName 日本製鉄, got %s", resp.SymbolName)
	}
	if resp.CurrentPrice != 2408.0 {
		t.Errorf("Expected CurrentPrice 2408.0, got %f", resp.CurrentPrice)
	}

	// Check time parsing
	expectedTime, _ := time.Parse(time.RFC3339, "2022-04-04T15:00:00+09:00")
	if !expectedTime.Equal(resp.CurrentPriceTime) {
		t.Errorf("Expected CurrentPriceTime %v, got %v", expectedTime, resp.CurrentPriceTime)
	}

	// Check board
	if resp.Sell1.Price != 2409.0 {
		t.Errorf("Expected Sell1.Price 2409.0, got %f", resp.Sell1.Price)
	}
	if resp.Sell1.Qty != 1500.0 {
		t.Errorf("Expected Sell1.Qty 1500.0, got %f", resp.Sell1.Qty)
	}
	if resp.Sell1.Sign != "0101" {
		t.Errorf("Expected Sell1.Sign 0101, got %s", resp.Sell1.Sign)
	}
	if !expectedTime.Equal(resp.Sell1.Time) {
		t.Errorf("Expected Sell1.Time %v, got %v", expectedTime, resp.Sell1.Time)
	}

	if resp.Sell2.Price != 2410.0 {
		t.Errorf("Expected Sell2.Price 2410.0, got %f", resp.Sell2.Price)
	}

	if resp.Buy1.Price != 2407.0 {
		t.Errorf("Expected Buy1.Price 2407.0, got %f", resp.Buy1.Price)
	}

	if resp.OverSellQty != 50000.0 {
		t.Errorf("Expected OverSellQty 50000.0, got %f", resp.OverSellQty)
	}
}
