package sniper

// PerformanceTracker tracks wins, losses, trade count, and realized PnL.
// It acts as a simple in-memory storage (memory) to record and query the
// cumulative performance metrics of each sniper during process execution.
type PerformanceTracker struct {
	performance map[string]Performance
}

func NewPerformanceTracker() *PerformanceTracker {
	return &PerformanceTracker{
		performance: make(map[string]Performance),
	}
}

func (pet *PerformanceTracker) RecordPnL(sniperID string, pnl float64) {
	perf := pet.performance[sniperID]
	perf.RealizedPnL += pnl
	perf.Trades++
	if pnl > 0 {
		perf.Wins++
	} else if pnl < 0 {
		perf.Losses++
	}
	pet.performance[sniperID] = perf
}

func (pet *PerformanceTracker) Get(sniperID string) Performance {
	return pet.performance[sniperID]
}
