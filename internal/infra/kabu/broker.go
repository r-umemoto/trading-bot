// internal/infra/kabu/broker.go
package kabu

import (
	"context"
	"fmt"
	"trading-bot/internal/domain/market"
)

// KabuOrderBroker はカブコムAPIを使って注文を実行します
type KabuOrderBroker struct {
	client      *KabuClient
	apiPassword string
}

func NewKabuOrderBroker(client *KabuClient) *KabuOrderBroker {
	return &KabuOrderBroker{client: client}
}

// SendOrder は market.OrderBroker の実装です
func (b *KabuOrderBroker) SendOrder(ctx context.Context, req market.OrderRequest) (string, error) {
	side := "1" // 売
	if req.Action == market.Buy {
		side = "2" // 買
	}

	kabReq := OrderRequest{
		Password:       b.apiPassword,
		Symbol:         req.Symbol,
		Exchange:       1,
		SecurityType:   1,
		Side:           side,
		Qty:            int(req.Qty),
		FrontOrderType: 10, // 成行
		Price:          0,
	}

	fmt.Printf("発注完了 side:%s, qty: %f", side, req.Qty)

	resp, err := b.client.SendOrder(kabReq)
	if err != nil {
		return "", fmt.Errorf("カブコムAPI発注失敗: %w", err)
	}

	return resp.OrderId, nil
}

// CancelOrder は market.OrderBroker の実装です
func (b *KabuOrderBroker) CancelOrder(ctx context.Context, orderID string) error {
	req := CancelRequest{OrderID: orderID, Password: b.apiPassword}
	_, err := b.client.CancelOrder(req)
	if err != nil {
		return fmt.Errorf("キャンセル失敗 (ResultCode: %s)", orderID)
	}
	return nil
}

func (b *KabuOrderBroker) GetOrders(ctx context.Context) ([]market.Order, error) {
	orders, err := b.client.GetOrders()
	if err != nil {
		return nil, fmt.Errorf("注文取得失敗)")
	}
	return orders, nil
}

func (b *KabuOrderBroker) GetPositions(ctx context.Context, product market.ProductType) ([]market.Position, error) {
	arg := ProductMargin
	if product != market.ProductMargin {
		// 現状は信用取引しかしてない
		return nil, fmt.Errorf("prodcutが不正です %d", product)
	}
	positions, err := b.client.GetPositions(arg)
	if err != nil {
		return nil, fmt.Errorf("建玉取得失敗: %d)", product)
	}

	decodePositons := make([]market.Position, 0, len(positions))
	for _, pos := range positions {
		decodePositons = append(decodePositons, market.Position{
			Symbol: pos.Symbol,
			Qty:    pos.LeavesQty,
		})
	}

	return decodePositons, nil
}
