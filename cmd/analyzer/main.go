package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath" // 🌟 追加
	"sort"
)

type LogEntry struct {
	Msg         string  `json:"msg"`
	Symbol      string  `json:"symbol"`
	Sniper      string  `json:"sniper"`
	ExitReason  string  `json:"exit_reason"`
	Pnl         float64 `json:"pnl"`
	HoldTimeSec float64 `json:"hold_time_sec"`
	QueueTimeMs int64   `json:"queue_time_ms"`
}

type ExitStat struct {
	Count           int
	PnlSum, HoldSum float64
}
type SymbolStat struct {
	Count, WinCount int
	PnlSum          float64
	PnlDist         map[float64]int
}
type QueueStat struct {
	Count             int
	TimeSum, Min, Max int64
}

func main() {
	logFile := flag.String("file", "", "解析する単一のログファイルパス")
	logDir := flag.String("dir", "", "解析するログファイルが含まれるディレクトリパス")
	flag.Parse()

	if *logFile == "" && *logDir == "" {
		fmt.Println("使用方法:")
		fmt.Println("  単一ファイル: go run cmd/analyzer/main.go -file <path_to_jsonl>")
		fmt.Println("  ディレクトリ一括: go run cmd/analyzer/main.go -dir <path_to_dir>")
		return
	}

	var filesToProcess []string
	if *logFile != "" {
		filesToProcess = append(filesToProcess, *logFile)
	}
	if *logDir != "" {
		entries, err := os.ReadDir(*logDir)
		if err != nil {
			fmt.Printf("ディレクトリの読み込みに失敗しました: %v\n", err)
			return
		}
		for _, e := range entries {
			if !e.IsDir() && filepath.Ext(e.Name()) == ".jsonl" {
				filesToProcess = append(filesToProcess, filepath.Join(*logDir, e.Name()))
			}
		}
	}

	symbolStats := make(map[string]*SymbolStat)
	queueStats := make(map[string]*QueueStat)
	symbolExitStats := make(map[string]map[string]*ExitStat)

	for _, path := range filesToProcess {
		processFile(path, symbolStats, queueStats, symbolExitStats)
	}

	syms := make([]string, 0, len(symbolStats))
	for sym := range symbolStats {
		syms = append(syms, sym)
	}
	sort.Strings(syms)

	fmt.Println("=== 銘柄・戦略別 パフォーマンス詳細 ===")
	for _, sym := range syms {
		stat := symbolStats[sym]
		if stat.Count == 0 {
			continue
		}
		fmt.Printf("\n【対象: %s】\n", sym)
		fmt.Printf("  取引数: %d, 勝率: %.1f%%, 合計損益: %.1f (平均: %.2f)\n",
			stat.Count, float64(stat.WinCount)/float64(stat.Count)*100, stat.PnlSum, stat.PnlSum/float64(stat.Count))

		// 決済理由別の詳細
		fmt.Println("  [決済理由・子戦略別の内訳]")
		reasons := make([]string, 0, len(symbolExitStats[sym]))
		for r := range symbolExitStats[sym] {
			reasons = append(reasons, r)
		}
		sort.Strings(reasons)
		for _, r := range reasons {
			es := symbolExitStats[sym][r]
			fmt.Printf("    - %-25s: %3d回, 損益: %8.1f, 平均保有: %5.1fs\n",
				r, es.Count, es.PnlSum, es.HoldSum/float64(es.Count))
		}

		// 約定待ち時間
		if qs, ok := queueStats[sym]; ok && qs.Count > 0 {
			fmt.Printf("  [約定待ち時間] 平均: %dms, 最小: %dms, 最大: %dms\n",
				qs.TimeSum/int64(qs.Count), qs.Min, qs.Max)
		}

		// PnL分布
		fmt.Print("  [損益分布] ")
		pnlValues := make([]float64, 0, len(stat.PnlDist))
		for p := range stat.PnlDist {
			pnlValues = append(pnlValues, p)
		}
		sort.Float64s(pnlValues)
		for _, p := range pnlValues {
			fmt.Printf("%.1f(%d) ", p, stat.PnlDist[p])
		}
		fmt.Println()
	}
}

func processFile(path string, symbolStats map[string]*SymbolStat, queueStats map[string]*QueueStat, symbolExitStats map[string]map[string]*ExitStat) {
	file, err := os.Open(path)
	if err != nil {
		fmt.Printf("警告: ファイル %s の読み込みに失敗: %v\n", path, err)
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var entry LogEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}

		switch entry.Msg {
		case "POSITION_CLOSED":
			stratKey := entry.Symbol + " [" + entry.Sniper + "]"
			if _, ok := symbolStats[stratKey]; !ok {
				symbolStats[stratKey] = &SymbolStat{PnlDist: make(map[float64]int)}
			}
			s := symbolStats[stratKey]
			s.Count++
			s.PnlSum += entry.Pnl
			if entry.Pnl > 0 {
				s.WinCount++
			}
			s.PnlDist[entry.Pnl]++

			if symbolExitStats[stratKey] == nil {
				symbolExitStats[stratKey] = make(map[string]*ExitStat)
			}
			reason := entry.ExitReason
			if reason == "" {
				reason = "(不明)"
			}
			if symbolExitStats[stratKey][reason] == nil {
				symbolExitStats[stratKey][reason] = &ExitStat{}
			}
			stat := symbolExitStats[stratKey][reason]
			stat.Count++
			stat.PnlSum += entry.Pnl
			stat.HoldSum += entry.HoldTimeSec

		case "FILLED":
			stratKey := entry.Symbol + " [" + entry.Sniper + "]"
			// QueueTimeMs が 0 の場合は統計に含めない
			if entry.QueueTimeMs == 0 {
				continue
			}
			if _, ok := queueStats[stratKey]; !ok {
				queueStats[stratKey] = &QueueStat{Min: math.MaxInt64, Max: 0}
			}
			q := queueStats[stratKey]
			q.Count++
			q.TimeSum += entry.QueueTimeMs
			if entry.QueueTimeMs < q.Min {
				q.Min = entry.QueueTimeMs
			}
			if entry.QueueTimeMs > q.Max {
				q.Max = entry.QueueTimeMs
			}
		}
	}
}


