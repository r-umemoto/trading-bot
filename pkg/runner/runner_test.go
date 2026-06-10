package runner_test

import (
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/r-umemoto/trading-bot/pkg/runner"
)

func TestRunBacktest_Success(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Prepare dummy CSV data
	csvContent := `Time,Symbol,Price,TradingVolume,VWAP,BestAskPrice,BestAskQty,BestBidPrice,BestBidQty,CurrentPriceStatus
09:00:00.000,7203,2500.0,10000.0,2499.5,2501.0,500.0,2500.0,800.0,1
09:00:01.000,7203,2502.0,15000.0,2500.5,2503.0,600.0,2502.0,900.0,1
`
	csvPath := filepath.Join(tempDir, "all_20260409.csv")
	if err := os.WriteFile(csvPath, []byte(csvContent), 0644); err != nil {
		t.Fatalf("failed to write temp CSV: %v", err)
	}

	// 2. Prepare dummy portfolio config
	portfolioContent := `[
		{
			"symbol": "7203",
			"name": "Toyota",
			"exchange": 1,
			"sector": "輸送用機器",
			"enabled": true
		}
	]`
	portfolioPath := filepath.Join(tempDir, "portfolio.json")
	if err := os.WriteFile(portfolioPath, []byte(portfolioContent), 0644); err != nil {
		t.Fatalf("failed to write temp portfolio: %v", err)
	}

	// 3. Prepare dummy operations config
	operationsContent := `[
		{
			"type": "default",
			"id": "TestOp_7203",
			"params": {
				"symbol": "7203",
				"strategies": ["sample"]
			}
		}
	]`
	operationsPath := filepath.Join(tempDir, "operations.json")
	if err := os.WriteFile(operationsPath, []byte(operationsContent), 0644); err != nil {
		t.Fatalf("failed to write temp operations: %v", err)
	}

	// 4. Save and override command line flags/args
	oldArgs := os.Args
	defer func() {
		os.Args = oldArgs
		// Clean up backtest logs
		os.RemoveAll("backtest_logs")
	}()

	os.Args = []string{
		"cmd",
		"-csv", csvPath,
		"-portfolio", portfolioPath,
		"-operations", operationsPath,
		"-execution-model", "touch",
		"-latency", "0",
	}

	// Reset CommandLine flags to allow parsing again
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)

	// Run backtest
	err := runner.RunBacktest()
	if err != nil {
		t.Fatalf("RunBacktest failed: %v", err)
	}
}

func TestRunBacktest_FileNotFound(t *testing.T) {
	oldArgs := os.Args
	defer func() {
		os.Args = oldArgs
	}()

	os.Args = []string{
		"cmd",
		"-csv", "non_existent.csv",
		"-portfolio", "non_existent.json",
	}

	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)

	err := runner.RunBacktest()
	if err == nil {
		t.Fatal("expected RunBacktest to fail with missing files")
	}
}

func TestRunBot_LoadConfigFailure(t *testing.T) {
	// Clean environment variables that would allow loading to succeed or make it fail predictably
	oldBroker := os.Getenv("BROKER_TYPE")
	os.Setenv("BROKER_TYPE", "") // Cause envconfig validation error or default config
	defer os.Setenv("BROKER_TYPE", oldBroker)

	// Also set temporary paths to missing files to trigger error
	oldPortfolio := os.Getenv("PORTFOLIO_PATH")
	oldOperations := os.Getenv("OPERATIONS_PATH")
	os.Setenv("PORTFOLIO_PATH", "missing_portfolio.json")
	os.Setenv("OPERATIONS_PATH", "missing_operations.json")
	defer func() {
		os.Setenv("PORTFOLIO_PATH", oldPortfolio)
		os.Setenv("OPERATIONS_PATH", oldOperations)
	}()

	err := runner.RunBot()
	if err == nil {
		t.Fatal("expected RunBot to return an error when configuration files are missing")
	}
}

func TestRunBot_BuildEngineFailure(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Mock API server returning 400 for Token to trigger BuildEngine failure
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	// 2. Set environments to trigger RunBot to point to our failing server
	oldBroker := os.Getenv("BROKER_TYPE")
	oldAPIURL := os.Getenv("KABU_API_URL")
	oldPassword := os.Getenv("KABU_PASSWORD")
	oldPortfolio := os.Getenv("PORTFOLIO_PATH")
	oldOperations := os.Getenv("OPERATIONS_PATH")

	os.Setenv("BROKER_TYPE", "kabu")
	os.Setenv("KABU_API_URL", server.URL)
	os.Setenv("KABU_PASSWORD", "test-password")

	// 3. Prepare valid portfolio and operations JSON files
	portfolioContent := `[{"symbol":"7203","enabled":true}]`
	portfolioPath := filepath.Join(tempDir, "portfolio.json")
	_ = os.WriteFile(portfolioPath, []byte(portfolioContent), 0644)
	os.Setenv("PORTFOLIO_PATH", portfolioPath)

	operationsContent := `[]`
	operationsPath := filepath.Join(tempDir, "operations.json")
	_ = os.WriteFile(operationsPath, []byte(operationsContent), 0644)
	os.Setenv("OPERATIONS_PATH", operationsPath)

	defer func() {
		os.Setenv("BROKER_TYPE", oldBroker)
		os.Setenv("KABU_API_URL", oldAPIURL)
		os.Setenv("KABU_PASSWORD", oldPassword)
		os.Setenv("PORTFOLIO_PATH", oldPortfolio)
		os.Setenv("OPERATIONS_PATH", oldOperations)
	}()

	err := runner.RunBot()
	if err == nil {
		t.Fatal("expected RunBot to fail due to API token retrieval failure during engine build")
	}
}
