package strategy

import (
	"trading-bot/internal/sniper/brain"
)

// KillSwitch は他の戦略をラップし、発動時に強制決済シグナルを出すデコレーター
type KillSwitch struct {
	MainLogic   LogicNode // 包み込まれる本来の戦略
	IsTriggered bool      // キルスイッチが押されたか
	HasPosition bool      // 現在建玉を持っているか（全決済のため）
	Quantity    int
}

// 本来の戦略を渡してキルスイッチ付き戦略を作る
func NewKillSwitch(mainLogic LogicNode, qty int) *KillSwitch {
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
		return brain.Signal{Action: brain.ActionSell, Quantity: k.Quantity}
	}

	return brain.Signal{Action: brain.ActionHold}
}

func (k *KillSwitch) Evaluate(price float64) brain.Signal {
	// 🚨 キルスイッチ発動中！
	if k.IsTriggered {
		// 既にキルスイッチ起動済みの場合は気絶しておく
		return brain.Signal{Action: brain.ActionHold}
	}

	// 🕊️ 平常時は、包み込んでいる本来の戦略に判断を丸投げする
	sig := k.MainLogic.Evaluate(price)

	// 本来の戦略が出したシグナルを見て、ポジション状態を同期しておく
	switch sig.Action {
	case brain.ActionBuy:
		k.HasPosition = true
	case brain.ActionSell:
		k.HasPosition = false
	}

	return sig
}
