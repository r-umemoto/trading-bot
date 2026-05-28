package usecase

import (
	"context"
	"fmt"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
)

// SniperNest は特定の銘柄（Symbol）における「狙撃陣地」です。
// 市場からのイベント（Tick, Orders）を受け取り、配下のスナイパーたちを指揮します。
type SniperNest struct {
	SymbolCode string
	Spotter    *sniper.Spotter
	Snipers    []*sniper.Sniper
	Gateway    market.MarketGateway
}

func NewSniperNest(
	code string,
	spotter *sniper.Spotter,
	snipers []*sniper.Sniper,
	gw market.MarketGateway,
) *SniperNest {
	return &SniperNest{
		SymbolCode: code,
		Spotter:    spotter,
		Snipers:    snipers,
		Gateway:    gw,
	}
}

// Start は市場イベント受信ループを起動します。
func (n *SniperNest) Start(ctx context.Context, chs market.SymbolChannels) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case t := <-chs.Tick:
				for _, s := range n.Snipers {
					obs := n.Spotter.PrepareObservation(s.ID, t)
					bullet := s.Tick(obs)
					if bullet.HasOrder() || bullet.HasCancel() {
						go n.fire(ctx, s, bullet)
					}
				}
			case ords := <-chs.Order:
				n.Spotter.Update(ords, time.Now())
				// Note: HandleIFD logic is now handled by the gateway
			}
		}
	}()
}

// fire は発注・キャンセルを実行し、APIのレスポンスを待機してスナイパーの状態を更新します。
// このメソッドは goroutine として実行されることを想定しています。
func (n *SniperNest) fire(ctx context.Context, s *sniper.Sniper, b sniper.Bullet) {
	if b.HasCancel() {
		err := n.Gateway.CancelOrder(ctx, b.CancelOrderID)
		if err != nil {
			fmt.Printf("キャンセル失敗 (ID: %s): %v\n", b.CancelOrderID, err)
		}
	}

	if b.HasOrder() {
		n.Spotter.RecordBullet(s.ID, b)
		updatedOrder, err := n.Gateway.SendOrder(ctx, order.SendOrderInput{Order: b.Order, Request: *b.Request})
		if err != nil {
			fmt.Printf("発注失敗 (Symbol: %s): %v\n", n.SymbolCode, err)
			s.FailSendingOrder(b.Order)
			return
		}
		// 成功時: APIが発行した本物のOrderIDに更新
		s.UpdateOrderID(b.Order, updatedOrder.ID)
	}
}

// ForceExit は配下の全スナイパーを強制終了させます
func (n *SniperNest) ForceExit() {
	for _, s := range n.Snipers {
		s.ForceExit()
	}
}

// GetSymbolCode はこの陣地の対象銘柄コードを返します
func (n *SniperNest) GetSymbolCode() string {
	return n.SymbolCode
}

// GetActiveOrders は配下の全スナイパーのアクティブな注文をすべて集約して返します
func (n *SniperNest) GetActiveOrders() []*order.Order {
	var allOrders []*order.Order
	for _, s := range n.Snipers {
		allOrders = append(allOrders, s.GetActiveOrders()...)
	}
	return allOrders
}
