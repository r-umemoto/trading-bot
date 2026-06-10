package storage_test

import (
	"encoding/csv"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
	"github.com/r-umemoto/trading-bot/pkg/infra/kabu/storage"
)

func TestCSVLogger(t *testing.T) {
	tempDir := t.TempDir()

	symbol := "7203"
	date := "20260610"

	logger, err := storage.NewCSVLogger(symbol, date, tempDir)
	if err != nil {
		t.Fatalf("failed to create CSVLogger: %v", err)
	}

	testTick := tick.Tick{
		Symbol:            symbol,
		Price:             2500.5,
		TradingVolume:     100000,
		VWAP:              2499.8,
		CurrentPriceTime:  time.Date(2026, 6, 10, 9, 0, 5, 123000000, time.Local),
		BestAsk:           tick.FirstQuote{Price: 2501.0, Qty: 500},
		BestBid:           tick.FirstQuote{Price: 2500.0, Qty: 800},
		SellBoard:         []tick.Quote{{Price: 2502.0, Qty: 300}},
		BuyBoard:          []tick.Quote{{Price: 2499.0, Qty: 400}},
		CurrentPriceStatus: tick.PRICE_STATUS_CURRENT,
		CurrentPriceChangeStatus: tick.PRICE_CHANGE_UP,
		OpeningPrice:      2480.0,
		TradingValue:      250000000.0,
		MarketOrderSellQty: 1000,
		MarketOrderBuyQty:  1200,
		OverSellQty:        5000,
		UnderBuyQty:        6000,
	}

	logger.Log(testTick)

	// Wait a moment for background goroutine to process the tick
	time.Sleep(50 * time.Millisecond)
	logger.Close()

	// Verify the file content
	expectedFile := filepath.Join(tempDir, symbol+"_"+date+".csv")
	file, err := os.Open(expectedFile)
	if err != nil {
		t.Fatalf("failed to open generated CSV file: %v", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	
	// Read header
	header, err := reader.Read()
	if err != nil {
		t.Fatalf("failed to read header: %v", err)
	}
	if len(header) == 0 || header[0] != "Time" {
		t.Errorf("unexpected header: %v", header)
	}

	// Read record
	record, err := reader.Read()
	if err != nil {
		t.Fatalf("failed to read record: %v", err)
	}

	if len(record) != len(header) {
		t.Errorf("record column count %d does not match header column count %d", len(record), len(header))
	}

	// Verify crucial fields
	if record[0] != "09:00:05.123" {
		t.Errorf("expected time '09:00:05.123', got: %s", record[0])
	}
	if record[1] != symbol {
		t.Errorf("expected symbol '%s', got: %s", symbol, record[1])
	}
	if record[2] != "2500.5" {
		t.Errorf("expected price '2500.5', got: %s", record[2])
	}
}

func TestCSVLogger_EmptyDirectoryError(t *testing.T) {
	// Trying to write to a path where a file already exists instead of directory
	tempFile, err := os.CreateTemp("", "csv_logger_err")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tempFile.Name())
	tempFile.Close()

	// Passing tempFile's path as output directory should fail because MkdirAll will fail or opening file inside it will fail
	_, err = storage.NewCSVLogger("7203", "20260610", tempFile.Name())
	if err == nil {
		t.Fatal("expected NewCSVLogger to fail when outputDir is an existing file")
	}
}

func TestCSVLogger_AppendMode(t *testing.T) {
	tempDir := t.TempDir()
	symbol := "7203"
	date := "20260610"

	// 1. Write first tick
	logger1, err := storage.NewCSVLogger(symbol, date, tempDir)
	if err != nil {
		t.Fatalf("failed to create logger1: %v", err)
	}
	tick1 := tick.Tick{Symbol: symbol, Price: 100}
	logger1.Log(tick1)
	time.Sleep(50 * time.Millisecond)
	logger1.Close()

	// 2. Write second tick using append
	logger2, err := storage.NewCSVLogger(symbol, date, tempDir)
	if err != nil {
		t.Fatalf("failed to create logger2: %v", err)
	}
	tick2 := tick.Tick{Symbol: symbol, Price: 200}
	logger2.Log(tick2)
	time.Sleep(50 * time.Millisecond)
	logger2.Close()

	// 3. Read back
	expectedFile := filepath.Join(tempDir, symbol+"_"+date+".csv")
	file, err := os.Open(expectedFile)
	if err != nil {
		t.Fatalf("failed to open file: %v", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	// Header
	_, _ = reader.Read()

	// Record 1
	rec1, err := reader.Read()
	if err != nil {
		t.Fatalf("failed to read rec1: %v", err)
	}
	if rec1[2] != "100" {
		t.Errorf("expected 100, got %s", rec1[2])
	}

	// Record 2
	rec2, err := reader.Read()
	if err != nil {
		t.Fatalf("failed to read rec2: %v", err)
	}
	if rec2[2] != "200" {
		t.Errorf("expected 200, got %s", rec2[2])
	}

	// Should be no more records
	_, err = reader.Read()
	if err != io.EOF {
		t.Errorf("expected EOF, got: %v", err)
	}
}
