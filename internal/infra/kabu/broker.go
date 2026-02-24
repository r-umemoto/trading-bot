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
	side := "1"     // 売
	cashMargin := 2 // 新規
	if req.Action == market.Buy {
		cashMargin = 3 // 返却
		side = "2"     // 買
	}

	kabReq := OrderRequest{
		Symbol:             req.Symbol,
		Exchange:           int(req.Exchange),
		SecurityType:       int(req.SecurityType),
		Side:               side,
		CashMargin:         cashMargin,
		MarginTradeType:    int(req.MarginTradeType),
		AccountType:        int(req.AccountType),
		ExpireDay:          0,
		Qty:                req.Qty,
		FrontOrderType:     int32(req.OrderType), // 指値
		Price:              req.Price,
		DelivType:          int32(req.DelivType),
		ClosePositionOrder: int32(req.ClosePositionOrder),
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
	domainOrders := make([]market.Order, 0, len(orders))
	for _, order := range orders {
		action := market.Buy
		if order.Side == SIDE_SELL {
			action = market.Sell
		}
		o := market.NewOrder(order.ID, order.Symbol, action, order.Price, order.CumQty)
		for _, excution := range order.Details {
			o.AddExecution(
				market.Execution{
					ID:    excution.ID,
					Price: excution.Price,
					Qty:   excution.Qty,
				},
			)
		}
	}
	return domainOrders, nil
}

func (b *KabuOrderBroker) GetPositions(ctx context.Context, product market.ProductType) ([]market.Position, error) {
	arg := ProductMargin
	if product != market.PRODUCT_MARGIN {
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
			Symbol:    pos.Symbol,
			LeavesQty: pos.LeavesQty,
		})
	}

	return decodePositons, nil
}
