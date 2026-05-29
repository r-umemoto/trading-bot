package usecase

import (
	"fmt"

	"github.com/r-umemoto/trading-bot/pkg/domain/service"
)

// ReportPresenter は取引成績（PerformanceReport）を人間が読める形に出力（コンソール表示・CSVファイル保存）するプレゼンターです。
type ReportPresenter struct{}

func NewReportPresenter() *ReportPresenter {
	return &ReportPresenter{}
}

func (rp *ReportPresenter) PrintPerformanceReport(report *service.PerformanceReport) {
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
}
