package usecase

import (
	"context"
	"log/slog"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/service"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

// SniperNest は特定の銘柄（ターゲット）を監視し、狙撃（発注）を行うスナイパーたちの潜伏陣地（アクター）です
type SniperNest struct {
	SymbolCode string
	Spotter    *sniper.Spotter // 🌟 事実管理
	Snipers    []*sniper.Sniper
	TickCh     <-chan tick.Tick
	OrderCh    <-chan order.Orders
	dispatcher *service.OrderDispatcher
}

// NewSniperNest は新しい SniperNest を生成します
func NewSniperNest(
	code string,
	spotter *sniper.Spotter,
	snipers []*sniper.Sniper,
	tc <-chan tick.Tick,
	oc <-chan order.Orders,
	d *service.OrderDispatcher,
) *SniperNest {
	return &SniperNest{
		SymbolCode: code,
		Spotter:    spotter,
		Snipers:    snipers,
		TickCh:     tc,
		OrderCh:    oc,
		dispatcher: d,
	}
}

// Start はこの陣地での価格監視・約定監視ループ（Actor Loop）を起動します
func (n *SniperNest) Start(ctx context.Context) {
	slog.Info("Setting up SniperNest", slog.String("symbol", n.SymbolCode), slog.Int("sniper_count", len(n.Snipers)))
	go func() {
		for {
			select {
			case <-ctx.Done():
				slog.Info("Dismantling SniperNest due to context done", slog.String("symbol", n.SymbolCode))
				return
			case t, ok := <-n.TickCh:
				if !ok {
					slog.Warn("SniperNest tick channel closed", slog.String("symbol", n.SymbolCode))
					return
				}
				n.ExecuteTick(ctx, t)
			case report, ok := <-n.OrderCh:
				if !ok {
					slog.Warn("SniperNest order channel closed", slog.String("symbol", n.SymbolCode))
					return
				}
				n.ExecuteExecutionReport(ctx, report)
			}
		}
	}()
}

// ExecuteTick はターゲットの最新価格更新（Tick）を受け取り、陣地内の各スナイパーに同期的・直列的にトリガーを引かせます
func (n *SniperNest) ExecuteTick(ctx context.Context, t tick.Tick) {
	// 1. Spotter に状況を整理させる
	obs := n.Spotter.PrepareObservation(t)

	for _, s := range n.Snipers {
		// 2. スナイパーに整理された事実を渡し、判断させる
		bullet := s.Tick(obs)

		// 3. 弾丸を発射（ディスパッチャへ送信）
		if bullet.HasOrder() || bullet.HasCancel() {
			// 新規発注の場合は Spotter にも追跡対象として加える
			if bullet.HasOrder() {
				n.Spotter.AddOrder(bullet.Order)
			}
			n.dispatcher.Submit(s, bullet)
		}
	}
}

// ExecuteExecutionReport は最新の注文レポートを受け取り、陣地内の各スナイパーと注文状態の同期を行います
func (n *SniperNest) ExecuteExecutionReport(ctx context.Context, report order.Orders) {
	// 1. 事実（Spotter）を最新のレポートで更新
	n.Spotter.Update(report, time.Now())

	// 2. 更新後の状況を Sniper に伝えて、必要ならアクション（IFD発火等）を起こさせる
	// (現在は Tick に集約しているが、即時性が必要な場合はここで s.SyncOrders(obs) を呼ぶ)
	obs := n.Spotter.PrepareObservation(tick.Tick{}) // Tickは空でも、ポジション情報があれば十分な場合がある
	for _, s := range n.Snipers {
		bullet := s.SyncOrders(obs)
		if bullet.HasOrder() || bullet.HasCancel() {
			if bullet.HasOrder() {
				n.Spotter.AddOrder(bullet.Order)
			}
			n.dispatcher.Submit(s, bullet)
		}
	}
}
