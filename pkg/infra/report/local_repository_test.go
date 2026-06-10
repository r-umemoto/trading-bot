package report_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/report"
	reportinfra "github.com/r-umemoto/trading-bot/pkg/infra/report"
)

func TestLocalRepository_Save(t *testing.T) {
	tempDir := t.TempDir()

	repo := reportinfra.NewLocalRepository(tempDir)
	if repo == nil {
		t.Fatal("expected NewLocalRepository to return a non-nil repository")
	}

	testReport := &report.DailyReport{
		Date:      "2026-06-10",
		UpdatedAt: time.Now().UTC(),
		Total: report.AggregatedPerformance{
			Name:        "total",
			Trades:      5,
			Wins:        3,
			Losses:      2,
			RealizedPnL: 15000,
		},
	}

	ctx := context.Background()
	err := repo.Save(ctx, testReport)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify the file was created
	expectedFile := filepath.Join(tempDir, "daily_2026-06-10.json")
	data, err := os.ReadFile(expectedFile)
	if err != nil {
		t.Fatalf("failed to read generated report file: %v", err)
	}

	// Unmarshal and verify
	var savedReport report.DailyReport
	err = json.Unmarshal(data, &savedReport)
	if err != nil {
		t.Fatalf("failed to unmarshal saved report JSON: %v", err)
	}

	if savedReport.Date != testReport.Date {
		t.Errorf("expected Date %s, got %s", testReport.Date, savedReport.Date)
	}
	if savedReport.Total.Trades != testReport.Total.Trades {
		t.Errorf("expected Trades %d, got %d", testReport.Total.Trades, savedReport.Total.Trades)
	}
}

func TestLocalRepository_SaveError(t *testing.T) {
	// Create a file at the target path instead of a directory
	tempFile, err := os.CreateTemp("", "local_repo_err")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tempFile.Name())
	tempFile.Close()

	// If we use the tempFile path as output directory, MkdirAll should fail
	repo := reportinfra.NewLocalRepository(tempFile.Name())
	testReport := &report.DailyReport{
		Date: "2026-06-10",
	}

	ctx := context.Background()
	err = repo.Save(ctx, testReport)
	if err == nil {
		t.Fatal("expected Save to fail when outputDir is a file path")
	}
}
