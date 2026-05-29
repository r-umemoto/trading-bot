package report

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/r-umemoto/trading-bot/pkg/domain/report"
)

type LocalRepository struct {
	outputDir string
}

func NewLocalRepository(outputDir string) *LocalRepository {
	return &LocalRepository{outputDir: outputDir}
}

func (l *LocalRepository) Save(ctx context.Context, r *report.DailyReport) error {
	if err := os.MkdirAll(l.outputDir, 0755); err != nil {
		return err
	}

	filePath := filepath.Join(l.outputDir, "daily_"+r.Date+".json")
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filePath, data, 0644)
}
