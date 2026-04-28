package strategy

import (
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/brain"
)

// KillSwitchState は KillSwitch が保持する銘柄ごとの状態
type KillSwitchState struct {
	IsTriggered bool
	HasPosition bool
	InnerState  StrategyState
}

// KillSwitch は他の戦略をラップし、発動時に強制決済シグナルを出すデコレーター
type KillSwitch struct {
	MainLogic Strategy // 包み込まれる本来の戦略
	Quantity  float64
	State     KillSwitchState
}

// NewKillSwitch は本来の戦略を渡してキルスイッチ付き戦略を作ります
func NewKillSwitch(mainLogic Strategy, qty float64) Strategy {
	return &KillSwitch{
		MainLogic: mainLogic,
		Quantity:  qty,
	}
}

func (k *KillSwitch) Name() string {
	return "KillSwitch(" + k.MainLogic.Name() + ")"
}

// 外部（main.goのCtrl+Cなど）から手動でキルスイッチを起動する
// 注: 状態を分離したため、どの銘柄のKillSwitchをActivateするかを指定する必要があります。
// SniperがKillSwitchableを解釈する際にこのStateを渡すように設計変更が必要ですが、
// 一旦インターフェースに合わせて修正します。
func (k *KillSwitch) Activate() brain.Signal {
	k.State.IsTriggered = true

	if k.State.HasPosition {
		k.State.HasPosition = false
		return brain.Signal{Action: brain.ACTION_SELL, Quantity: k.Quantity}
	}

	return brain.Signal{Action: brain.ACTION_HOLD}
}

func (k *KillSwitch) Evaluate(input StrategyInput) brain.Signal {
	// 🚨 キルスイッチ発動中！
	if k.State.IsTriggered {
		// 既にキルスイッチ起動済みの場合は気絶しておく
		return brain.Signal{Action: brain.ACTION_HOLD}
	}

	// 🕊️ 平常時は、包み込んでいる本来の戦略に判断を丸投げする
	sig := k.MainLogic.Evaluate(input)

	// 本来の戦略が出したシグナルを見て、ポジション状態を同期しておく
	switch sig.Action {
	case brain.ACTION_BUY:
		k.State.HasPosition = true
	case brain.ACTION_SELL:
		k.State.HasPosition = false
	}

	return sig
}
