package strategy

import (
	"trading-bot/internal/domain/sniper/brain"
)

// KillSwitch は他の戦略をラップし、発動時に強制決済シグナルを出すデコレーター
type KillSwitch struct {
	MainLogic   LogicNode // 包み込まれる本来の戦略
	IsTriggered bool      // キルスイッチが押されたか
	HasPosition bool      // 現在建玉を持っているか（全決済のため）
	Quantity    float64
}

// 本来の戦略を渡してキルスイッチ付き戦略を作る
func NewKillSwitch(mainLogic LogicNode, qty float64) *KillSwitch {
	return &KillSwitch{
		MainLogic:   mainLogic,
		IsTriggered: false,
		HasPosition: false,
		Quantity:    qty,
	}
}

// 外部（main.goのCtrl+Cなど）から手動でキルスイッチを起動する
func (k *KillSwitch) Activate() brain.Signal {
	k.IsTriggered = true

	if k.HasPosition {
		k.HasPosition = false
		return brain.Signal{Action: brain.ACTION_SELL, Quantity: k.Quantity}
	}

	return brain.Signal{Action: brain.ACTION_HOLD}
}

func (k *KillSwitch) Evaluate(input StrategyInput) brain.Signal {
	// 🚨 キルスイッチ発動中！
	if k.IsTriggered {
		// 既にキルスイッチ起動済みの場合は気絶しておく
		return brain.Signal{Action: brain.ACTION_HOLD}
	}

	// 🕊️ 平常時は、包み込んでいる本来の戦略に判断を丸投げする
	sig := k.MainLogic.Evaluate(input)

	// 本来の戦略が出したシグナルを見て、ポジション状態を同期しておく
	switch sig.Action {
	case brain.ACTION_BUY:
		k.HasPosition = true
	case brain.ACTION_SELL:
		k.HasPosition = false
	}

	return sig
}
