// internal/infra/kabu/broker.go
package kabu

import (
	"context"
	"fmt"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
)

func NewMarketGateway(client *KabuClient, wsClient *WSClient) *MarketGateway {
	return &MarketGateway{
		client:              client,
		wsClient:            wsClient,
		processedExecutions: make(map[string]bool),
	}
}

// MarketGateway はHTTPプロトコルを用いたREST API操作を担当します
type MarketGateway struct {
	client              *KabuClient
	wsClient            *WSClient
	processedExecutions map[string]bool // 通知済みの注文IDを記録し、重複検知を防ぐ
}

// Start は market.MarketGateway の実装です
func (m *MarketGateway) Start(ctx context.Context) (<-chan market.Tick, <-chan market.ExecutionReport, error) {
	priceCh := make(chan market.Tick, 100)
	execCh := make(chan market.ExecutionReport, 10)

	// 1. 株価のWebSocketを裏側で起動（既存の WebSocket 処理）
	go m.startWebSocketLoop(ctx, priceCh)

	// 2. 約定のポーリングを裏側で起動（先ほど話していた Watcher 処理）
	go m.startPollingLoop(ctx, execCh)

	// 呼び出し側（Engine）には、美しく整えられた2つのチャネルだけを返す
	return priceCh, execCh, nil
}

// SendOrder は market.MarketGateway (Orderer) の実装です
func (m *MarketGateway) SendOrder(ctx context.Context, req market.OrderRequest) (string, error) {
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
		Exchange:           m.toKabuExchageType(req.Exchange),
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

	resp, err := m.client.SendOrder(kabReq)
	if err != nil {
		return "", fmt.Errorf("カブコムAPI発注失敗: %w", err)
	}

	return resp.OrderId, nil
}

// CancelOrder は market.MarketGateway (Orderer) の実装です
func (m *MarketGateway) CancelOrder(ctx context.Context, orderID string) error {
	req := CancelRequest{OrderID: orderID}
	_, err := m.client.CancelOrder(req)
	if err != nil {
		return fmt.Errorf("キャンセル失敗 (ResultCode: %s)", orderID)
	}
	return nil
}

func (m *MarketGateway) GetOrders(ctx context.Context) ([]market.Order, error) {
	orders, err := m.client.GetOrders()
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
		domainOrders = append(domainOrders, o)
	}

	return domainOrders, nil
}

func (m *MarketGateway) GetPositions(ctx context.Context, product market.ProductType) ([]market.Position, error) {
	arg := ProductMargin
	if product != market.PRODUCT_MARGIN {
		// 現状は信用取引しかしてない
		return nil, fmt.Errorf("prodcutが不正です %d", product)
	}
	positions, err := m.client.GetPositions(arg)
	if err != nil {
		return nil, fmt.Errorf("建玉取得失敗: %d)", product)
	}

	decodePositons := make([]market.Position, 0, len(positions))
	for _, pos := range positions {
		decodePositons = append(decodePositons, market.Position{
			ExecutionID: pos.ExecutionID,
			Symbol:      pos.Symbol,
			Exchange:    m.toMarketExchageType(pos.Exchange),
			Action:      m.toMakerAction(pos.Side),
			TradeType:   m.toMakerTradeType(pos.MarginTradeType),
			AccountType: m.toAccountType(pos.AccountType),
			LeavesQty:   pos.LeavesQty,
			Price:       pos.Price,
		})
	}

	return decodePositons, nil
}

func (m *MarketGateway) toMakerAction(side string) market.Action {
	switch side {
	case string(SIDE_SELL):
		return market.ACTION_SELL
	case string(SIDE_BUY):
		return market.ACTION_BUY
	default:
		return ""
	}
}

func (m *MarketGateway) toMakerTradeType(tradeType int32) market.MarginTradeType {
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

func (m *MarketGateway) toAccountType(accountType int32) market.AccountType {
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

func (m *MarketGateway) startPollingLoop(ctx context.Context, execCh chan market.ExecutionReport) {
	ticker := time.NewTicker(3 * time.Second) // 3秒間隔でポーリング
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			// 注入されたFetcherを使って注文一覧を取得
			orders, err := m.GetOrders(ctx)
			if err != nil {
				fmt.Printf("ポーリングエラー: %v\n", err)
				continue
			}

			// 1. 注文(Order)のループ
			for _, order := range orders {

				// 2. さらに明細(Details)のループを回す！
				for _, detail := range order.Executions {

					// 約定IDが空の明細（単なる「受付済」などのステータス履歴）はスキップ
					if detail.ID == "" {
						continue
					}

					// 🌟 注文IDではなく「約定ID」で通知済みかを判定する
					if m.processedExecutions[detail.ID] {
						continue
					}

					// 約定イベントを生成してチャネルに送信
					execCh <- market.ExecutionReport{
						OrderID:     order.ID,
						ExecutionID: detail.ID, // レポートにも約定IDを持たせる
						Symbol:      order.Symbol,
						Action:      order.Action,
						Price:       detail.Price, // 👈 Details側の「実際の約定単価」
						Qty:         detail.Qty,   // 👈 Details側の「実際の約定数量」
					}

					// 🌟 処理完了として「約定ID」を記録する
					m.processedExecutions[detail.ID] = true
				}
			}
		}
	}
}

func (s *MarketGateway) startWebSocketLoop(ctx context.Context, tickCh chan market.Tick) {
	// 既存のWebSocketクライアントを起動
	rawCh := make(chan PushMessage)
	go s.wsClient.Listen(rawCh)

	// 🔄 変換層（アダプター処理）
	go func() {
		defer close(tickCh)
		counter := 0
		for {
			select {
			case <-ctx.Done():
				// システム終了時は安全にゴルーチンを抜ける
				return
			case msg := <-rawCh:
				counter++
				if counter > 100 {
					counter = 0
					fmt.Printf("現在の指標 %+v\n", msg)
				}
				tickCh <- market.Tick{
					Symbol: msg.Symbol,
					Price:  msg.CurrentPrice,
					VWAP:   msg.VWAP,
				}
			}
		}
	}()
}

func (m *MarketGateway) RegisterSymbol(ctx context.Context, req market.ResisterSymbolRequest) error {

	clientReq := RegisterSymbolRequest{
		Symbols: []RegisterSymbolsItem{
			{
				Symbol:   req.Symbol,
				Exchange: m.toKabuExchageType(req.Exchange),
			},
		},
	}

	_, err := m.client.RegisterSymbol(clientReq)
	if err != nil {
		return fmt.Errorf("銘柄登録失敗: %+v)", req)
	}
	return nil
}

func (m *MarketGateway) UnregisterSymbolAll(ctx context.Context) error {
	_, err := m.client.UnregisterSymbolAll()
	if err != nil {
		return fmt.Errorf("銘柄登録全解除失敗)")
	}
	return nil
}

func (m *MarketGateway) toMarketExchageType(exchange ExchageType) market.ExchangeMarket {
	switch exchange {
	case EXCHANGE_TYPE_TOSHO_PLS:
		return market.EXCHANGE_TOSHO
	case EXCHANGE_TYPE_TOSHO_SOR:
		return market.EXCHANGE_SOR
	}
	return market.EXCHANGE_SOR
}

func (m *MarketGateway) toKabuExchageType(exchange market.ExchangeMarket) ExchageType {
	switch exchange {
	case market.EXCHANGE_TOSHO:
		return EXCHANGE_TYPE_TOSHO_PLS
	case market.EXCHANGE_SOR:
		return EXCHANGE_TYPE_TOSHO_SOR
	}
	return EXCHANGE_TYPE_TOSHO_SOR
}
