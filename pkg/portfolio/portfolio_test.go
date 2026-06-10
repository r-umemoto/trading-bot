package portfolio_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/portfolio"
)

func TestLoadFromJSON_Success(t *testing.T) {
	tempDir := t.TempDir()
	jsonContent := `[
		{
			"symbol": "8306",
			"name": "MUFG",
			"exchange": 1,
			"sector": "銀行業",
			"enabled": true
		}
	]`
	
	filePath := filepath.Join(tempDir, "portfolio.json")
	if err := os.WriteFile(filePath, []byte(jsonContent), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	targets, err := portfolio.LoadFromJSON(filePath)
	if err != nil {
		t.Fatalf("LoadFromJSON failed: %v", err)
	}

	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}

	target := targets[0]
	if target.Symbol != "8306" {
		t.Errorf("expected Symbol 8306, got %s", target.Symbol)
	}
	if target.Name != "MUFG" {
		t.Errorf("expected Name MUFG, got %s", target.Name)
	}
	if target.Exchange != order.EXCHANGE_TOSHO {
		t.Errorf("expected Exchange TOSHO, got %v", target.Exchange)
	}
	if target.Sector != "銀行業" {
		t.Errorf("expected Sector 銀行業, got %s", target.Sector)
	}
	if !target.Enabled {
		t.Error("expected Enabled to be true")
	}
}

func TestLoadFromJSON_FileNotFound(t *testing.T) {
	_, err := portfolio.LoadFromJSON("non_existent_file.json")
	if err == nil {
		t.Fatal("expected LoadFromJSON to return error for non-existent file")
	}
}

func TestLoadFromJSON_InvalidJSON(t *testing.T) {
	tempDir := t.TempDir()
	jsonContent := `{invalid json}`
	
	filePath := filepath.Join(tempDir, "portfolio.json")
	if err := os.WriteFile(filePath, []byte(jsonContent), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	_, err := portfolio.LoadFromJSON(filePath)
	if err == nil {
		t.Fatal("expected LoadFromJSON to fail with invalid JSON")
	}
}

func TestLoadOperationsFromJSON_Success(t *testing.T) {
	tempDir := t.TempDir()
	jsonContent := `[
		{
			"type": "pair_trading",
			"id": "PairOp_7201_7267",
			"params": {
				"symbol_a": "7201",
				"symbol_b": "7267",
				"threshold": 1.5,
				"qty": 100.0
			}
		}
	]`
	
	filePath := filepath.Join(tempDir, "operations.json")
	if err := os.WriteFile(filePath, []byte(jsonContent), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	ops, err := portfolio.LoadOperationsFromJSON(filePath)
	if err != nil {
		t.Fatalf("LoadOperationsFromJSON failed: %v", err)
	}

	if len(ops) != 1 {
		t.Fatalf("expected 1 operation, got %d", len(ops))
	}

	op := ops[0]
	if op.Type != "pair_trading" {
		t.Errorf("expected Type pair_trading, got %s", op.Type)
	}
	if op.ID != "PairOp_7201_7267" {
		t.Errorf("expected ID PairOp_7201_7267, got %s", op.ID)
	}
	
	symbolA, ok := op.Params["symbol_a"].(string)
	if !ok || symbolA != "7201" {
		t.Errorf("expected symbol_a '7201', got %v", op.Params["symbol_a"])
	}
}

func TestLoadOperationsFromJSON_FileNotFound(t *testing.T) {
	_, err := portfolio.LoadOperationsFromJSON("non_existent_file.json")
	if err == nil {
		t.Fatal("expected LoadOperationsFromJSON to return error for non-existent file")
	}
}

func TestLoadOperationsFromJSON_InvalidJSON(t *testing.T) {
	tempDir := t.TempDir()
	jsonContent := `{invalid json}`
	
	filePath := filepath.Join(tempDir, "operations.json")
	if err := os.WriteFile(filePath, []byte(jsonContent), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	_, err := portfolio.LoadOperationsFromJSON(filePath)
	if err == nil {
		t.Fatal("expected LoadOperationsFromJSON to fail with invalid JSON")
	}
}
