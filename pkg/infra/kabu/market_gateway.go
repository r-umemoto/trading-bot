// internal/infra/kabu/broker.go
package kabu

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"

	"github.com/r-umemoto/trading-bot/pkg/infra/kabu/api"
	"github.com/r-umemoto/trading-bot/pkg/infra/kabu/storage"
)

func NewMarketGateway(client *api.KabuClient, wsClient *api.WSClient) *MarketGateway {
	return &MarketGateway{
		client:   client,
		wsClient: wsClient,
	}
}

// MarketGateway はHTTPプロトコルを用いたREST API操作を担当します
type MarketGateway struct {
	client   *api.KabuClient
	wsClient *api.WSClient
}

// Start は market.MarketGateway の実装です
func (m *MarketGateway) Start(ctx context.Context) (<-chan market.Tick, <-chan market.OrdersReport, error) {
	priceCh := make(chan market.Tick, 100)
	orderCh := make(chan market.OrdersReport, 10)

	// 1. 株価のWebSocketを裏側で起動
	go m.startWebSocketLoop(ctx, priceCh)

	// 2. 注文のポーリングを裏側で起動
	go m.startPollingLoop(ctx, orderCh)

	return priceCh, orderCh, nil
}

// SendOrder は market.MarketGateway (Orderer) の実装です
func (m *MarketGateway) SendOrder(ctx context.Context, req market.OrderRequest) (string, error) {
	side := api.SIDE_SELL
	if req.Action == market.ACTION_BUY {
		side = api.SIDE_BUY
	}

	cashMargin := 2 // デフォルトは「新規」
	if req.ClosePositionOrder != market.CLOSE_POSITION_ORDER_NONE || len(req.ClosePositions) > 0 {
		cashMargin = 3 // 返済指示があれば「返済」
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

	// APIへリクエスト
	var closePositions []api.ClosePosition
	for _, cp := range req.ClosePositions {
		closePositions = append(closePositions, api.ClosePosition{
			HoldID: cp.HoldID,
			Qty:    cp.Qty,
		})
	}

	var closePositionOrder *int32
	if len(closePositions) == 0 && req.ClosePositionOrder != market.CLOSE_POSITION_ORDER_NONE {
		val := int32(req.ClosePositionOrder)
		closePositionOrder = &val
	}

	kabReq := api.OrderRequest{
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
		ClosePositionOrder: closePositionOrder,
		ClosePositions:     closePositions,
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
	req := api.CancelRequest{OrderID: orderID}
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
		if order.Side == api.SIDE_SELL {
			action = market.ACTION_SELL
		}

		// api.Order.State を market.OrderStatus にマッピング
		status := market.ORDER_STATUS_WAITING
		switch order.State {
		case 1, 2:
			status = market.ORDER_STATUS_WAITING
		case 3:
			status = market.ORDER_STATUS_IN_PROGRESS
		case 4:
			status = market.ORDER_STATUS_CANCEL_SENT
		case 5:
			if order.CumQty >= order.OrderQty {
				status = market.ORDER_STATUS_FILLED
			} else {
				// Details を走査して理由を探す
				foundReason := false
				for _, detail := range order.Details {
					if detail.RecType == 3 || detail.RecType == 7 {
						status = market.ORDER_STATUS_EXPIRED
						foundReason = true
						break
					}
					if detail.RecType == 6 {
						status = market.ORDER_STATUS_CANCELED
						foundReason = true
						break
					}
				}
				if !foundReason {
					status = market.ORDER_STATUS_CANCELED // Fallback
				}
			}
		}

		o := market.NewOrder(order.ID, order.Symbol, action, order.Price, order.OrderQty)
		o.Status = status

		for _, execution := range order.Details {
			// RecType が 8 (約定) の場合のみ Execution として追加
			if execution.RecType != 8 || execution.ID == "" {
				continue
			}
			o.AddExecution(
				market.Execution{
					ID:    execution.ID,
					Price: execution.Price,
					Qty:   execution.Qty,
				},
			)
		}
		domainOrders = append(domainOrders, o)
	}

	return domainOrders, nil
}

func (m *MarketGateway) GetPositions(ctx context.Context, product market.ProductType) ([]market.Position, error) {
	arg := api.ProductMargin
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
	case string(api.SIDE_SELL):
		return market.ACTION_SELL
	case string(api.SIDE_BUY):
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

func (m *MarketGateway) startPollingLoop(ctx context.Context, orderCh chan market.OrdersReport) {
	ticker := time.NewTicker(3 * time.Second) // 3秒間隔でポーリング
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			orders, err := m.GetOrders(ctx)
			if err != nil {
				fmt.Printf("ポーリングエラー: %v\n", err)
				continue
			}

			// 注文一覧をそのまま通知。差分検知や約定の特定は Sniper 側で行う
			orderCh <- market.OrdersReport{
				Orders: orders,
			}
		}
	}
}

func (s *MarketGateway) startWebSocketLoop(ctx context.Context, tickCh chan market.Tick) {
	// 既存のWebSocketクライアントを起動
	rawCh := make(chan api.PushMessage)
	go s.wsClient.Listen(rawCh)

	today := time.Now().Format("20060102")
	// "all" というプレフィックスで、./data ディレクトリに保存
	logger, err := storage.NewCSVLogger("all", today, "./data")
	if err != nil {
		log.Fatalf("ロガーの初期化に失敗しました: %v", err)
	}

	// 🔄 変換層（アダプター処理）
	go func() {
		defer close(tickCh)
		// goroutine終了時（システム終了時など）にログファイルを閉じる
		defer logger.Close()
		for {
			select {
			case <-ctx.Done():
				// システム終了時は安全にゴルーチンを抜ける
				return
			case msg := <-rawCh:
				// 板情報を集約
				sellBoard := []market.Quote{
					{Price: msg.Sell1.Price, Qty: msg.Sell1.Qty},
					{Price: msg.Sell2.Price, Qty: msg.Sell2.Qty},
					{Price: msg.Sell3.Price, Qty: msg.Sell3.Qty},
					{Price: msg.Sell4.Price, Qty: msg.Sell4.Qty},
					{Price: msg.Sell5.Price, Qty: msg.Sell5.Qty},
					{Price: msg.Sell6.Price, Qty: msg.Sell6.Qty},
					{Price: msg.Sell7.Price, Qty: msg.Sell7.Qty},
					{Price: msg.Sell8.Price, Qty: msg.Sell8.Qty},
					{Price: msg.Sell9.Price, Qty: msg.Sell9.Qty},
					{Price: msg.Sell10.Price, Qty: msg.Sell10.Qty},
				}
				buyBoard := []market.Quote{
					{Price: msg.Buy1.Price, Qty: msg.Buy1.Qty},
					{Price: msg.Buy2.Price, Qty: msg.Buy2.Qty},
					{Price: msg.Buy3.Price, Qty: msg.Buy3.Qty},
					{Price: msg.Buy4.Price, Qty: msg.Buy4.Qty},
					{Price: msg.Buy5.Price, Qty: msg.Buy5.Qty},
					{Price: msg.Buy6.Price, Qty: msg.Buy6.Qty},
					{Price: msg.Buy7.Price, Qty: msg.Buy7.Qty},
					{Price: msg.Buy8.Price, Qty: msg.Buy8.Qty},
					{Price: msg.Buy9.Price, Qty: msg.Buy9.Qty},
					{Price: msg.Buy10.Price, Qty: msg.Buy10.Qty},
				}

				tick := market.NewTick(
					msg.Symbol,
					msg.CurrentPrice,
					msg.VWAP,
					msg.TradingVolume,
					msg.CurrentPriceTime,
					market.FirstQuote{
						Price: msg.Sell1.Price,
						Qty:   msg.Sell1.Qty,
						Time:  msg.Sell1.Time,
						Sign:  msg.Sell1.Sign,
					},
					market.FirstQuote{
						Price: msg.Buy1.Price,
						Qty:   msg.Buy1.Qty,
						Time:  msg.Buy1.Time,
						Sign:  msg.Buy1.Sign,
					},
					sellBoard,
					buyBoard,
					s.toPriceStatus(msg.CurrentPriceStatus),
					s.toPriceChangeStatus(msg.CurrentPriceChangeStatus),
					msg.OpeningPrice,
					msg.TradingValue,
					msg.MarketOrderSellQty,
					msg.MarketOrderBuyQty,
					msg.OverSellQty,
					msg.UnderBuyQty,
				)
				logger.Log(tick)
				tickCh <- tick
			}
		}
	}()
}

func (m *MarketGateway) RegisterSymbol(ctx context.Context, req market.ResisterSymbolRequest) error {

	clientReq := api.RegisterSymbolRequest{
		Symbols: []api.RegisterSymbolsItem{
			{
				Symbol:   req.Symbol,
				Exchange: m.toRegisterSymbolKabuExchageType(req.Exchange),
			},
		},
	}

	_, err := m.client.RegisterSymbol(clientReq)
	if err != nil {
		return fmt.Errorf("銘柄登録失敗: %+v)", req)
	}
	return nil
}

func (m *MarketGateway) GetSymbol(ctx context.Context, symbol string, exchange market.ExchangeMarket) (market.Symbol, error) {
	resp, err := m.client.GetSymbol(symbol, m.toKabuExchageType(exchange))
	if err != nil {
		return market.Symbol{}, fmt.Errorf("銘柄情報取得失敗: %w", err)
	}

	return market.Symbol{
		Code:            resp.Symbol,
		Name:            resp.SymbolName,
		PriceRangeGroup: market.PriceRangeGroup(resp.PriceRangeGroup),
	}, nil
}

func (m *MarketGateway) UnregisterSymbolAll(ctx context.Context) error {
	_, err := m.client.UnregisterSymbolAll()
	if err != nil {
		return fmt.Errorf("銘柄登録全解除失敗)")
	}
	return nil
}

func (m *MarketGateway) toMarketExchageType(exchange api.ExchageType) market.ExchangeMarket {
	switch exchange {
	case api.EXCHANGE_TYPE_TOSHO:
		return market.EXCHANGE_TOSHO
	case api.EXCHANGE_TYPE_TOSHO_PLS:
		return market.EXCHANGE_TOSHO_PLUS
	case api.EXCHANGE_TYPE_TOSHO_SOR:
		return market.EXCHANGE_SOR
	}
	return market.EXCHANGE_SOR
}

func (m *MarketGateway) toKabuExchageType(exchange market.ExchangeMarket) api.ExchageType {
	switch exchange {
	case market.EXCHANGE_TOSHO:
		return api.EXCHANGE_TYPE_TOSHO
	case market.EXCHANGE_TOSHO_PLUS:
		return api.EXCHANGE_TYPE_TOSHO_PLS
	case market.EXCHANGE_SOR:
		return api.EXCHANGE_TYPE_TOSHO_SOR
	}
	return api.EXCHANGE_TYPE_TOSHO_SOR
}

func (m *MarketGateway) toRegisterSymbolKabuExchageType(exchange market.ExchangeMarket) api.ExchageType {
	switch exchange {
	case market.EXCHANGE_TOSHO, market.EXCHANGE_TOSHO_PLUS: // API仕様で東証+はない
		return api.EXCHANGE_TYPE_TOSHO
	case market.EXCHANGE_SOR:
		return api.EXCHANGE_TYPE_TOSHO_SOR
	}
	return api.EXCHANGE_TYPE_TOSHO_SOR
}
func (m *MarketGateway) toPriceStatus(kabuStatus int) market.PriceStatus {
	// カブコム仕様: 1:現値, 3:寄付, 4:前引, 5:大引, 2:特別気配, 6:特成...
	// ドメイン仕様にマッピング
	switch kabuStatus {
	case 1:
		return market.PRICE_STATUS_CURRENT
	case 3:
		return market.PRICE_STATUS_OPENING
	case 4:
		return market.PRICE_STATUS_PRE_CLOSE
	case 5:
		return market.PRICE_STATUS_CLOSE
	case 2, 6, 7, 8, 9: // 特別気配系
		return market.PRICE_STATUS_SPECIAL
	default:
		return market.PRICE_STATUS_NONE
	}
}

func (m *MarketGateway) toPriceChangeStatus(kabuStatus string) market.PriceChangeStatus {
	// カブコム仕様: '0000': 変わらず, '0056': 上昇, '0057': 下落...
	switch kabuStatus {
	case "0056":
		return market.PRICE_CHANGE_UP
	case "0057":
		return market.PRICE_CHANGE_DOWN
	case "0000":
		return market.PRICE_CHANGE_UNCHANGED
	default:
		return market.PRICE_CHANGE_NONE
	}
}
