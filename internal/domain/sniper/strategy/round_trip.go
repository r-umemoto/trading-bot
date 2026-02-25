package strategy

import "trading-bot/internal/domain/sniper/brain"

// ---------------------------------------------------
// ③ 複合戦略：買いと売りをセットにした包括的戦略 (RoundTripStrategy)
// ---------------------------------------------------

// ★ strategyパッケージ専用のローカルインターフェース！
// （sniper.Brainと全く同じ形だけど、お互い全く関係ない）
type LogicNode interface {
	Evaluate(input StrategyInput) brain.Signal
}

type RoundTripStrategy struct {
	EntryStrategy LogicNode
	ExitStrategy  LogicNode
	HasPosition   bool
}

func NewRoundTrip(entry, exit LogicNode) *RoundTripStrategy {
	return &RoundTripStrategy{
		EntryStrategy: entry,
		ExitStrategy:  exit,
		HasPosition:   false, // 最初は建玉を持たない状態からスタート
	}
}

// 単体の戦略と全く同じ Evaluate を持っているため、Sniper からは透過的に扱える
func (s *RoundTripStrategy) Evaluate(input StrategyInput) brain.Signal {
	if !s.HasPosition {
		// 建玉がない場合：買い戦略に判断を委譲
		sig := s.EntryStrategy.Evaluate(input)
		if sig.Action == brain.ACTION_BUY {
			s.HasPosition = true // 買いシグナルが出たら「持っている」状態へ遷移
		}
		return sig
	} else {
		// 建玉がある場合：売り戦略に判断を委譲
		sig := s.ExitStrategy.Evaluate(input)
		if sig.Action == brain.ACTION_SELL {
			s.HasPosition = false // 売りシグナルが出たら「持っていない」状態へ戻る
		}
		return sig
	}
}
