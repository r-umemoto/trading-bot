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

func (l *LocalRepository) Load(ctx context.Context, date string) (*report.DailyReport, error) {
	filePath := filepath.Join(l.outputDir, "daily_"+date+".json")
	return LoadFromFile(filePath)
}

// LoadFromFile は指定されたファイルパスからDailyReportを読み込みます。
func LoadFromFile(filePath string) (*report.DailyReport, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	var r report.DailyReport
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}

	return &r, nil
}
