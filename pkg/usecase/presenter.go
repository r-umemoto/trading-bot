package usecase

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/service"
)

// ReportPresenter は取引成績（PerformanceReport）を人間が読める形に出力（コンソール表示・CSVファイル保存）するプレゼンターです。
type ReportPresenter struct{}

func NewReportPresenter() *ReportPresenter {
	return &ReportPresenter{}
}

func (rp *ReportPresenter) PrintPerformanceReport(report *service.PerformanceReport, enableCSV bool) {
	printPerf := func(name string, p *service.AggregatedPerformance) {
		winRate := 0.0
		if p.Trades > 0 {
			winRate = float64(p.Wins) / float64(p.Trades) * 100
		}
		fmt.Printf("%-20s | 取引: %4d回 | 勝率: %5.1f%% (%4d勝 %4d敗) | 実現損益: %+10.0f 円 | 含み損益: %+10.0f 円 | 合計: %+10.0f 円\n",
			name, p.Trades, winRate, p.Wins, p.Losses, p.RealizedPnL, p.UnrealizedPnL, p.RealizedPnL+p.UnrealizedPnL)
	}

	fmt.Println("\n=============================================")
	fmt.Println("             トレード成績サマリー")
	fmt.Println("=============================================")

	fmt.Println("\n=== 1. 全体成績 (Total Performance) ===")
	printPerf("Total", report.Total)

	fmt.Println("\n=== 2. 銘柄別成績 (Performance by Symbol) ===")
	for _, p := range report.Symbols {
		printPerf(p.Name, p)
	}

	fmt.Println("\n=== 3. ストラテジー別成績 (Performance by Strategy) ===")
	for _, p := range report.Strats {
		printPerf(p.Name, p)
	}

	fmt.Println("\n=== 4. 銘柄 × ストラテジー相性 (Performance by Symbol + Strategy) ===")
	for _, p := range report.Combined {
		printPerf(p.Name, p)
	}
	fmt.Println("=============================================")

	if enableCSV {
		rp.SaveToCSV(report)
	}
}

func (rp *ReportPresenter) SaveToCSV(report *service.PerformanceReport) {
	outputDir := "data"
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		fmt.Printf("❌ ディレクトリ作成失敗: %v\n", err)
		return
	}

	now := time.Now().Format("20060102_150405")
	filename := fmt.Sprintf("performance_%s.csv", now)
	fullpath := filepath.Join(outputDir, filename)

	file, err := os.Create(fullpath)
	if err != nil {
		fmt.Printf("❌ CSV作成失敗: %v\n", err)
		return
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	// ヘッダー
	writer.Write([]string{"Type", "Name", "Trades", "Wins", "Losses", "WinRate", "RealizedPnL", "UnrealizedPnL", "TotalPnL"})

	writeLine := func(t, name string, p *service.AggregatedPerformance) {
		winRate := 0.0
		if p.Trades > 0 {
			winRate = float64(p.Wins) / float64(p.Trades) * 100
		}
		writer.Write([]string{
			t,
			name,
			strconv.Itoa(p.Trades),
			strconv.Itoa(p.Wins),
			strconv.Itoa(p.Losses),
			fmt.Sprintf("%.1f", winRate),
			fmt.Sprintf("%.0f", p.RealizedPnL),
			fmt.Sprintf("%.0f", p.UnrealizedPnL),
			fmt.Sprintf("%.0f", p.RealizedPnL+p.UnrealizedPnL),
		})
	}

	writeLine("Total", report.Total.Name, report.Total)
	for _, p := range report.Symbols {
		writeLine("Symbol", p.Name, p)
	}
	for _, p := range report.Strats {
		writeLine("Strategy", p.Name, p)
	}
	for _, p := range report.Combined {
		writeLine("SymbolStrategy", p.Name, p)
	}

	fmt.Printf("💾 成績をCSVに保存しました: %s\n", fullpath)
}
