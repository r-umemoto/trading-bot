package sniper

import (
	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

// FireAction はスナイパーの意思決定と、実行すべきアクションのペアです。
type FireAction struct {
	Sniper *Sniper
	Bullet Bullet
}

// SniperNest は特定の銘柄（Symbol）におけるスナイパーたちを束ねるドメイン集約（Aggregate Root）です。
// 非同期処理や外部のチャネル、物理的な通信には一切依存せず、同期的な純粋ドメインロジックのみをカプセル化します。
type SniperNest struct {
	SymbolCode string
	Spotter    *Spotter
	Snipers    []*Sniper
}

func NewSniperNest(code string, spotter *Spotter, snipers []*Sniper) *SniperNest {
	return &SniperNest{
		SymbolCode: code,
		Spotter:    spotter,
		Snipers:    snipers,
	}
}

// HandleTick は時価（Tick）の更新を受け取り、配下の各スナイパーに Observation を配分して意思決定を促します。
// アクション（発注・キャンセル）が必要な場合は FireAction を生成し、Spotter に注文弾（Bullet）を記録した上で返します。
func (n *SniperNest) HandleTick(t tick.Tick) []FireAction {
	var actions []FireAction
	for _, s := range n.Snipers {
		obs := n.Spotter.PrepareObservation(s.ID, t)
		bullet := s.Tick(obs)
		if bullet.HasOrder() || bullet.HasCancel() {
			if bullet.HasOrder() {
				n.Spotter.RecordBullet(s.ID, bullet)
			}
			actions = append(actions, FireAction{
				Sniper: s,
				Bullet: bullet,
			})
		}
	}
	return actions
}

// ForceExit は配下の全スナイパーに緊急撤退を命じます。
func (n *SniperNest) ForceExit() {
	for _, s := range n.Snipers {
		s.ForceExit()
	}
}

// GetSymbolCode は対象の銘柄コードを返します。
func (n *SniperNest) GetSymbolCode() string {
	return n.SymbolCode
}

// GetActiveOrders は配下の全スナイパーが追跡中の未完了注文を集約して返します。
func (n *SniperNest) GetActiveOrders() []*order.Order {
	var allOrders []*order.Order
	for _, s := range n.Snipers {
		allOrders = append(allOrders, s.GetActiveOrders()...)
	}
	return allOrders
}
