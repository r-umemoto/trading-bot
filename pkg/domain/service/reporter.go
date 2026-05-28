package service

import (
	"strings"

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

// PerformanceReport は取引成績の純粋なドメイン集集計結果（エンティティ / 値オブジェクト）です
type PerformanceReport struct {
	Total    *AggregatedPerformance
	Symbols  map[string]*AggregatedPerformance
	Strats   map[string]*AggregatedPerformance
	Combined map[string]*AggregatedPerformance
}

// GeneratePerformanceReport はターゲット群から成績を集計し、ドメインモデルを生成します (純粋関数)
func GeneratePerformanceReport(provider sniper.PerformanceProvider, targets []sniper.ReportableTarget, dataPool tick.DataPool) *PerformanceReport {
	perfMap := make(map[string]*AggregatedPerformance)
	symPerfMap := make(map[string]*AggregatedPerformance)
	stratPerfMap := make(map[string]*AggregatedPerformance)
	totalPerf := &AggregatedPerformance{Name: "Total"}

	for _, s := range targets {
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
		marketState := dataPool.GetState(symbolCode)
		if !marketState.LatestTick.CurrentPriceTime.IsZero() {
			unrealized = provider.GetUnrealizedPnL(targetID, marketState.LatestTick.Price)
		}

		// 成績を集計
		perf := provider.GetPerformance(targetID)
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

	return &PerformanceReport{
		Total:    totalPerf,
		Symbols:  symPerfMap,
		Strats:   stratPerfMap,
		Combined: perfMap,
	}
}

