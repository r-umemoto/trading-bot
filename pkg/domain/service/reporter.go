package service

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

type AggregatedPerformance struct {
	Name          string
	Trades        int
	Wins          int
	Losses        int
	RealizedPnL   float64
	UnrealizedPnL float64
}

type ReportableTarget interface {
	GetID() string
	GetSymbolCode() string
	GetStrategyName() string
}

// PerformanceReporter は全スナイパーの成績を集計し、出力およびCSV保存を行うレポート作成サービスです
type PerformanceReporter struct {
	provider sniper.PerformanceProvider
	targets  []ReportableTarget
	dataPool tick.DataPool
}

func NewPerformanceReporter(provider sniper.PerformanceProvider, targets []ReportableTarget, dataPool tick.DataPool) *PerformanceReporter {
	return &PerformanceReporter{
		provider: provider,
		targets:  targets,
		dataPool: dataPool,
	}
}

func (r *PerformanceReporter) PrintPerformanceReport(enableCSV bool) {
	// キー: "Symbol|StrategyName"
	perfMap := make(map[string]*AggregatedPerformance)
	symPerfMap := make(map[string]*AggregatedPerformance)
	stratPerfMap := make(map[string]*AggregatedPerformance)
	totalPerf := &AggregatedPerformance{Name: "Total"}

	for _, s := range r.targets {
		stratName := s.GetStrategyName()
		symbolCode := s.GetSymbolCode()
		targetID := s.GetID()
		key := symbolCode + "|" + stratName

		if perfMap[key] == nil {
			perfMap[key] = &AggregatedPerformance{Name: strings.Replace(key, "|", " x ", 1)}
		}
		if symPerfMap[symbolCode] == nil {
			symPerfMap[symbolCode] = &AggregatedPerformance{Name: symbolCode}
		}
		if stratPerfMap[stratName] == nil {
			stratPerfMap[stratName] = &AggregatedPerformance{Name: stratName}
		}

		// 含み損益の計算
		var unrealized float64
		marketState := r.dataPool.GetState(symbolCode)
		if !marketState.LatestTick.CurrentPriceTime.IsZero() {
			unrealized = r.provider.GetUnrealizedPnL(targetID, marketState.LatestTick.Price)
		}

		// 成績を集計
		perf := r.provider.GetPerformance(targetID)
		updatePerf := func(p *AggregatedPerformance) {
			p.Trades += perf.Trades
			p.Wins += perf.Wins
			p.Losses += perf.Losses
			p.RealizedPnL += perf.RealizedPnL
			p.UnrealizedPnL += unrealized // 最新の含み損益を使用
		}

		updatePerf(perfMap[key])
		updatePerf(symPerfMap[symbolCode])
		updatePerf(stratPerfMap[stratName])
		updatePerf(totalPerf)
	}

	printPerf := func(name string, p *AggregatedPerformance) {
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
	printPerf("Total", totalPerf)

	fmt.Println("\n=== 2. 銘柄別成績 (Performance by Symbol) ===")
	for _, p := range symPerfMap {
		printPerf(p.Name, p)
	}

	fmt.Println("\n=== 3. ストラテジー別成績 (Performance by Strategy) ===")
	for _, p := range stratPerfMap {
		printPerf(p.Name, p)
	}

	fmt.Println("\n=== 4. 銘柄 × ストラテジー相性 (Performance by Symbol + Strategy) ===")
	for _, p := range perfMap {
		printPerf(p.Name, p)
	}
	fmt.Println("=============================================")

	if enableCSV {
		r.saveToCSV(totalPerf, symPerfMap, stratPerfMap, perfMap)
	}
}

func (r *PerformanceReporter) saveToCSV(total *AggregatedPerformance, symbols map[string]*AggregatedPerformance, strats map[string]*AggregatedPerformance, combined map[string]*AggregatedPerformance) {
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

	writeLine := func(t, name string, p *AggregatedPerformance) {
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

	writeLine("Total", total.Name, total)
	for _, p := range symbols {
		writeLine("Symbol", p.Name, p)
	}
	for _, p := range strats {
		writeLine("Strategy", p.Name, p)
	}
	for _, p := range combined {
		writeLine("SymbolStrategy", p.Name, p)
	}

	fmt.Printf("💾 成績をCSVに保存しました: %s\n", fullpath)
}
