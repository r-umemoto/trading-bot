package usecase

import (
	"context"
	"fmt"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

// SniperNest は特定の銘柄（Symbol）における「狙撃陣地」です。
// 市場からのイベント（Tick, Orders）を受け取り、配下のスナイパーたちを指揮します。
type SniperNest struct {
	SymbolCode string
	Spotter    *sniper.Spotter
	Snipers    []*sniper.Sniper
	Channels   market.SymbolChannels
	Gateway    market.MarketGateway
}

func NewSniperNest(
	code string,
	spotter *sniper.Spotter,
	snipers []*sniper.Sniper,
	chs market.SymbolChannels,
	gw market.MarketGateway,
) *SniperNest {
	return &SniperNest{
		SymbolCode: code,
		Spotter:    spotter,
		Snipers:    snipers,
		Channels:   chs,
		Gateway:    gw,
	}
}

// Start は市場イベント受信ループを起動します。
func (n *SniperNest) Start(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case t := <-n.Channels.Tick:
				for _, s := range n.Snipers {
					obs := n.Spotter.PrepareObservation(s.ID, t)
					bullet := s.Tick(obs)
					if bullet.HasOrder() || bullet.HasCancel() {
						go n.fire(ctx, s, bullet)
					}
				}
			case ords := <-n.Channels.Order:
				n.Spotter.Update(ords, time.Now())
				for _, s := range n.Snipers {
					obs := n.Spotter.PrepareObservation(s.ID, tick.Tick{})
					bullet := s.HandleIFD(obs)
					if bullet.HasOrder() || bullet.HasCancel() {
						go n.fire(ctx, s, bullet)
					}
				}
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
		updatedOrder, err := n.Gateway.SendOrder(ctx, order.SendOrderInput{Order: *b.Order, Request: *b.Request})
		if err != nil {
			fmt.Printf("発注失敗 (Symbol: %s): %v\n", n.SymbolCode, err)
			s.FailSendingOrder(b.Order)
			return
		}
		// 成功時: APIが発行した本物のOrderIDに更新
		s.UpdateOrderID(b.Order, updatedOrder.ID)
	}
}
