package sniper

import (
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

// ReportableTarget はレポート出力の対象となるエンティティのインターフェースです。
type ReportableTarget interface {
	GetID() string
	GetSymbolCode() string
	GetStrategyName() string
}

// FireAction はスナイパーの意思決定と、実行すべきアクションのペアです。
type FireAction struct {
	SniperID string
	Bullet   Bullet
}

// SniperNest は特定の銘柄（Symbol）におけるスナイパーたちを束ねるドメイン集約（Aggregate Root）です。
// 非同期処理や外部のチャネル、物理的な通信には一切依存せず、同期的な純粋ドメインロジックのみをカプセラ化します。
type SniperNest struct {
	SymbolCode string
	spotter    *Spotter
	snipers    []*Sniper
}

func NewSniperNest(code string, spotter *Spotter, snipers []*Sniper) *SniperNest {
	return &SniperNest{
		SymbolCode: code,
		spotter:    spotter,
		snipers:    snipers,
	}
}

// HandleTick は時価（Tick）の更新を受け取り、配下の各スナイパーに Observation を配分して意思決定を促します。
// アクション（発注・キャンセル）が必要な場合は FireAction を生成し、Spotter に注文弾（Bullet）を記録した上で返します。
func (n *SniperNest) HandleTick(t tick.Tick) []FireAction {
	var actions []FireAction
	for _, s := range n.snipers {
		obs := n.spotter.PrepareObservation(s.ID, t)
		bullet := s.Tick(obs)
		if bullet.HasOrder() || bullet.HasCancel() {
			if bullet.HasOrder() {
				n.spotter.RecordBullet(s.ID, bullet)
			}
			actions = append(actions, FireAction{
				SniperID: s.ID,
				Bullet:   bullet,
			})
		}
	}
	return actions
}

// ForceExit は配下の全スナイパーに緊急撤退を命じます。
func (n *SniperNest) ForceExit() {
	for _, s := range n.snipers {
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
	for _, s := range n.snipers {
		allOrders = append(allOrders, s.GetActiveOrders()...)
	}
	return allOrders
}

// UpdateOrders は注文・約定レポートをもとに、内部の Spotter を更新します。
func (n *SniperNest) UpdateOrders(report order.Orders) {
	n.spotter.Update(report, time.Now())
}

// GetPerformance は指定したスナイパーの成績を取得します。
func (n *SniperNest) GetPerformance(sniperID string) Performance {
	return n.spotter.GetPerformance(sniperID)
}

// GetUnrealizedPnL は指定したスナイパーの含み損益を計算します。
func (n *SniperNest) GetUnrealizedPnL(sniperID string, currentPrice float64) float64 {
	return n.spotter.GetUnrealizedPnL(sniperID, currentPrice)
}

// GetExchanges はこのネスト内のスナイパーが動作する一意な取引所のリストを返します。
func (n *SniperNest) GetExchanges() []order.ExchangeMarket {
	seen := make(map[order.ExchangeMarket]bool)
	var list []order.ExchangeMarket
	for _, s := range n.snipers {
		if !seen[s.Exchange] {
			seen[s.Exchange] = true
			list = append(list, s.Exchange)
		}
	}
	return list
}

// GetReportableTargets は内部のスナイパー群を ReportableTarget インターフェースの型にキャストして返します。
func (n *SniperNest) GetReportableTargets() []ReportableTarget {
	var targets []ReportableTarget
	for _, s := range n.snipers {
		targets = append(targets, s)
	}
	return targets
}

// HasSniper は指定したスナイパーIDがこのネスト内に存在するか判定します。
func (n *SniperNest) HasSniper(sniperID string) bool {
	for _, s := range n.snipers {
		if s.ID == sniperID {
			return true
		}
	}
	return false
}

// FailSendingOrder は対象のスナイパーに発注失敗を通知します。
func (n *SniperNest) FailSendingOrder(sniperID string, ord *order.Order) {
	for _, s := range n.snipers {
		if s.ID == sniperID {
			s.FailSendingOrder(ord)
			break
		}
	}
}

// UpdateOrderID は対象のスナイパーが持つ注文IDを最新に更新します。
func (n *SniperNest) UpdateOrderID(sniperID string, ord *order.Order, newID string) {
	for _, s := range n.snipers {
		if s.ID == sniperID {
			s.UpdateOrderID(ord, newID)
			break
		}
	}
}

