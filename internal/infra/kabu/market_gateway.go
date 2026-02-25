// internal/infra/kabu/broker.go
package kabu

import (
	"context"
	"fmt"
	"trading-bot/internal/domain/market"
)

// KabuOrderBroker はカブコムAPIを使って注文を実行します
type MarketGateway struct {
	client *KabuClient
}

func NewKabuOrderBroker(client *KabuClient) *MarketGateway {
	return &MarketGateway{client: client}
}

// SendOrder は market.OrderBroker の実装です
func (b *MarketGateway) SendOrder(ctx context.Context, req market.OrderRequest) (string, error) {
	side := SIDE_SELL // 売
	cashMargin := 3   // 返却
	if req.Action == market.ACTION_BUY {
		cashMargin = 2  // 新規
		side = SIDE_BUY // 買
	}

	AccountType := 0
	switch req.AccountType {
	case market.ACCOUNT_SPECIAL:
		AccountType = 4
	}
	if AccountType == 0 {
		return "", fmt.Errorf("口座種別が不正です")
	}

	exchange := 0
	switch req.Exchange {
	case market.EXCHANGE_TOSHO:
		exchange = 1
	}
	if exchange == 0 {
		return "", fmt.Errorf("市場が不正です")
	}

	securityType := 0
	switch req.SecurityType {
	case market.SECURITY_TYPE_STOCK:
		securityType = 1
	}
	if securityType == 0 {
		return "", fmt.Errorf("商品が不正です")
	}

	tradeType := 0
	switch req.MarginTradeType {
	case market.TRADE_TYPE_GENERAL_DAY:
		tradeType = 3
	}
	if tradeType == 0 {
		return "", fmt.Errorf("取引種別が不正です")
	}

	orderType := 0
	switch req.OrderType {
	case market.ORDER_TYPE_MARKET:
		orderType = 10
	case market.ORDER_TYPE_LIMIT:
		orderType = 20
	}
	if orderType == 0 {
		return "", fmt.Errorf("注文種別が不正です")
	}

	deliverType := 0
	switch req.Action {
	case market.ACTION_BUY:
		if cashMargin == 1 {
			deliverType = 2
		}
	case market.ACTION_SELL:
		if cashMargin == 3 {
			deliverType = 2
		}
	}

	kabReq := OrderRequest{
		Symbol:             req.Symbol,
		Exchange:           exchange,
		SecurityType:       securityType,
		Side:               string(side),
		CashMargin:         cashMargin,
		MarginTradeType:    tradeType,
		AccountType:        AccountType,
		ExpireDay:          0,
		Qty:                req.Qty,
		FrontOrderType:     int32(orderType),
		Price:              req.Price,
		DelivType:          int32(deliverType),
		ClosePositionOrder: int32(req.ClosePositionOrder),
	}

	fmt.Printf("発注完了 %+v\n", kabReq)

	resp, err := b.client.SendOrder(kabReq)
	if err != nil {
		return "", fmt.Errorf("カブコムAPI発注失敗: %w", err)
	}

	return resp.OrderId, nil
}

// CancelOrder は market.OrderBroker の実装です
func (b *MarketGateway) CancelOrder(ctx context.Context, orderID string) error {
	req := CancelRequest{OrderID: orderID}
	_, err := b.client.CancelOrder(req)
	if err != nil {
		return fmt.Errorf("キャンセル失敗 (ResultCode: %s)", orderID)
	}
	return nil
}

func (b *MarketGateway) GetOrders(ctx context.Context) ([]market.Order, error) {
	orders, err := b.client.GetOrders()
	if err != nil {
		return nil, fmt.Errorf("注文取得失敗)")
	}
	domainOrders := make([]market.Order, 0, len(orders))
	for _, order := range orders {
		action := market.ACTION_BUY
		if order.Side == SIDE_SELL {
			action = market.ACTION_SELL
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

func (b *MarketGateway) GetPositions(ctx context.Context, product market.ProductType) ([]market.Position, error) {
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
			ExecutionID: pos.ExecutionID,
			Symbol:      pos.Symbol,
			Exchange:    b.toMarketExchange(pos.Exchange),
			Action:      b.toMakerAction(pos.Side),
			TradeType:   b.toMakerTradeType(pos.MarginTradeType),
			AccountType: b.toAccountType(pos.AccountType),
			LeavesQty:   pos.LeavesQty,
			Price:       pos.Price,
		})
	}

	return decodePositons, nil
}

func (b *MarketGateway) toMarketExchange(excahge int32) market.ExchangeMarket {
	switch excahge {
	case 1:
		return market.EXCHANGE_TOSHO
	default:
		return market.EXCHANGE_NONE
	}
}

func (b *MarketGateway) toMakerAction(side string) market.Action {
	switch side {
	case string(SIDE_SELL):
		return market.ACTION_SELL
	case string(SIDE_BUY):
		return market.ACTION_BUY
	default:
		return ""
	}
}

func (b *MarketGateway) toMakerTradeType(tradeType int32) market.MarginTradeType {
	switch tradeType {
	case 1:
		return market.TRADE_TYPE_SYSTEM
	case 2:
		return market.TRADE_TYPE_GENERAL
	case 3:
		return market.TRADE_TYPE_GENERAL_DAY
	default:
		return market.TRADE_TYPE_NONE
	}
}

func (b *MarketGateway) toAccountType(accountType int32) market.AccountType {
	switch accountType {
	case 2:
		return market.ACCOUNT_GENERAL
	case 4:
		return market.ACCOUNT_SPECIAL
	case 12:
		return market.ACCOUNT_CORPORATE
	default:
		return market.ACCOUNT_NONE
	}
}
