package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type LogEntry struct {
	Msg         string    `json:"msg"`
	Symbol      string    `json:"symbol"`
	Sniper      string    `json:"sniper"`
	ExitReason  string    `json:"exit_reason"`
	Pnl         float64   `json:"pnl"`
	HoldTimeSec float64   `json:"hold_time_sec"`
	QueueTimeMs int64     `json:"queue_time_ms"`
	EntryTime   time.Time `json:"entry_time"`
	ExitTime    time.Time `json:"exit_time"`
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

type HourlyStat struct {
	Count, WinCount int
	PnlSum          float64
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
	symbolHourlyStats := make(map[string]map[int]*HourlyStat)

	for _, path := range filesToProcess {
		processFile(path, symbolStats, queueStats, symbolExitStats, symbolHourlyStats)
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

		// 時間帯別成績
		fmt.Println("  [時間帯別成績 (Entry Hour)]")
		hours := make([]int, 0, len(symbolHourlyStats[sym]))
		for h := range symbolHourlyStats[sym] {
			hours = append(hours, h)
		}
		sort.Ints(hours)
		for _, h := range hours {
			hs := symbolHourlyStats[sym][h]
			winRate := 0.0
			if hs.Count > 0 {
				winRate = float64(hs.WinCount) / float64(hs.Count) * 100
			}
			fmt.Printf("    %02d:00 - %02d:59 | 取引: %2d回 | 勝率: %5.1f%% | 実現損益: %8.1f 円\n",
				h, h, hs.Count, winRate, hs.PnlSum)
		}

		// 決済理由別の詳細
		fmt.Println("  [決済理由別の内訳]")
		reasons := make([]string, 0, len(symbolExitStats[sym]))
		for r := range symbolExitStats[sym] {
			reasons = append(reasons, r)
		}
		sort.Strings(reasons)

		// 日本語ラベルへのマッピング
		labelMap := map[string]string{
			"TakeProfit": "(利確)",
			"StopLoss":   "(損切)",
			"TimeStop":   "(タイムストップ)",
			"LunchNoise": "(昼跨ぎによるノイズ)",
		}

		for _, r := range reasons {
			es := symbolExitStats[sym][r]
			label := r
			if l, ok := labelMap[r]; ok {
				label = l
			}
			fmt.Printf("    - %-20s: %3d回, 損益: %8.1f, 平均保有: %5.1fs\n",
				label, es.Count, es.PnlSum, es.HoldSum/float64(es.Count))
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

func processFile(path string, symbolStats map[string]*SymbolStat, queueStats map[string]*QueueStat, symbolExitStats map[string]map[string]*ExitStat, symbolHourlyStats map[string]map[int]*HourlyStat) {
	file, err := os.Open(path)
	if err != nil {
		fmt.Printf("警告: ファイル %s の読み込みに失敗: %v\n", path, err)
		return
	}
	defer file.Close()

	jst := time.FixedZone("JST", 9*60*60)

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

			// 時間帯別統計 (EntryTimeを使用)
			if !entry.EntryTime.IsZero() {
				if symbolHourlyStats[stratKey] == nil {
					symbolHourlyStats[stratKey] = make(map[int]*HourlyStat)
				}
				et := entry.EntryTime.In(jst)
				hour := et.Hour()
				if symbolHourlyStats[stratKey][hour] == nil {
					symbolHourlyStats[stratKey][hour] = &HourlyStat{}
				}
				hs := symbolHourlyStats[stratKey][hour]
				hs.Count++
				hs.PnlSum += entry.Pnl
				if entry.Pnl > 0 {
					hs.WinCount++
				}
			}

			// 決済理由別統計
			if symbolExitStats[stratKey] == nil {
				symbolExitStats[stratKey] = make(map[string]*ExitStat)
			}
			reason := entry.ExitReason
			if reason == "" {
				reason = "(不明)"
			}

			// 昼跨ぎノイズの判定 (11:30 - 12:30 を跨いでいるか)
			if isLunchCrossing(entry.EntryTime, entry.ExitTime, jst) {
				reason = "LunchNoise"
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

func isLunchCrossing(entryTime, exitTime time.Time, loc *time.Location) bool {
	if entryTime.IsZero() || exitTime.IsZero() {
		return false
	}
	et := entryTime.In(loc)
	xt := exitTime.In(loc)

	// 同じ日である前提（日を跨ぐトレードは今回想定外）
	if et.YearDay() != xt.YearDay() || et.Year() != xt.Year() {
		return false
	}

	// 11:30以前にエントリーし、12:30以降にエグジットしている
	lunchStart := time.Date(et.Year(), et.Month(), et.Day(), 11, 30, 0, 0, loc)
	lunchEnd := time.Date(et.Year(), et.Month(), et.Day(), 12, 30, 0, 0, loc)

	return et.Before(lunchStart) && xt.After(lunchEnd)
}
