package kabu

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"strconv"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/position"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"

	"github.com/r-umemoto/trading-bot/pkg/infra/kabu/api"
	"github.com/r-umemoto/trading-bot/pkg/infra/kabu/storage"
)

func NewMarketGateway(client *api.KabuClient, wsClient *api.WSClient) *MarketGateway {
	m := &MarketGateway{
		client:        client,
		wsClient:      wsClient,
		tickChannels:  make(map[string]chan tick.Tick),
		orderChannels: make(map[string]chan order.Orders),
		wsPushCh:      make(chan api.PushMessage, 1000),
	}
	kabuProvider := NewKabuHistoricalFeederProvider(m.client)
	m.dataPool = tick.NewDefaultDataPool(kabuProvider)
	m.dispatcher = NewOrderDispatcher(m)
	return m
}

type KabuClientInterface interface {
	GetOrders() ([]api.Order, error)
	SendOrder(req api.OrderRequest) (*api.OrderResponse, error)
	CancelOrder(req api.CancelRequest) (*api.CancelResponse, error)
	GetPositions(product api.ProductType) ([]api.Position, error)
	RegisterSymbol(req api.RegisterSymbolRequest) (*api.RegisterSymbolResponse, error)
	UnregisterSymbolAll() (*api.UnregisterSymbolAllResponse, error)
	GetSymbol(symbol string, exchange api.ExchageType) (*api.SymbolSuccess, error)
	GetBoard(symbol string) (*api.BoardResponse, error)
}

type MarketGateway struct {
	client        KabuClientInterface
	wsClient      *api.WSClient
	wsPushCh      chan api.PushMessage
	tickChannels  map[string]chan tick.Tick
	orderChannels map[string]chan order.Orders
	dataPool      tick.DataPool
	dispatcher    *OrderDispatcher
}

var _ market.MarketGateway = (*MarketGateway)(nil)
var _ Sender = (*MarketGateway)(nil)

func (m *MarketGateway) Listen(ctx context.Context) (*market.MarketChannels, error) {
	// 1. 各種ワーカーを起動
	go m.wsClient.Listen(m.wsPushCh)
	go m.startWebSocketLoop(ctx)
	go m.startPollingLoop(ctx)
	m.dispatcher.Start(ctx)

	// 2. チャネルを整理して返す
	ticks := make(map[string]<-chan tick.Tick)
	for sym, ch := range m.tickChannels {
		ticks[sym] = ch
	}
	orders := make(map[string]<-chan order.Orders)
	for sym, ch := range m.orderChannels {
		orders[sym] = ch
	}

	return &market.MarketChannels{
		Ticks:  ticks,
		Orders: orders,
	}, nil
}

func (m *MarketGateway) DataPool() tick.DataPool {
	return m.dataPool
}

// SendOrder は market.MarketGateway (Orderer) の実装です。
// 結果が出るまで内部的にブロックします（SniperNest等から非同期で呼ばれる想定）。
func (m *MarketGateway) SendOrder(ctx context.Context, input order.SendOrderInput) (order.Order, error) {
	resCh := m.dispatcher.Submit(input.Order.Symbol, &input.Order, &input.Request, "")
	select {
	case <-ctx.Done():
		return input.Order, ctx.Err()
	case res := <-resCh:
		if res.Error != nil {
			return input.Order, res.Error
		}
		if res.Order != nil {
			return *res.Order, nil
		}
		return input.Order, nil
	}
}

// SendOrderRaw は実際にAPIを叩く低レベルメソッドです
func (m *MarketGateway) SendOrderRaw(ctx context.Context, input order.SendOrderInput) (order.Order, error) {
	ord := input.Order
	req := input.Request

	side := api.SIDE_SELL
	if ord.Action == order.ACTION_BUY {
		side = api.SIDE_BUY
	}

	cashMargin := 2 // デフォルトは「新規」
	if req.ClosePositionOrder != order.CLOSE_POSITION_ORDER_NONE || len(req.ClosePositions) > 0 {
		cashMargin = 3 // 返済指示があれば「返済」
	}

	AccountType := 0
	switch req.AccountType {
	case order.ACCOUNT_SPECIAL:
		AccountType = 4
	}
	if AccountType == 0 {
		return ord, fmt.Errorf("口座種別が不正です")
	}

	securityType := 0
	switch req.SecurityType {
	case order.SECURITY_TYPE_STOCK:
		securityType = 1
	}
	if securityType == 0 {
		return ord, fmt.Errorf("商品が不正です")
	}

	tradeType := 0
	switch req.MarginTradeType {
	case order.TRADE_TYPE_SYSTEM:
		tradeType = 1
	case order.TRADE_TYPE_GENERAL:
		tradeType = 2
	case order.TRADE_TYPE_GENERAL_DAY:
		tradeType = 3
	}
	if tradeType == 0 {
		return ord, fmt.Errorf("取引種別が不正です (MarginTradeType: %d)", req.MarginTradeType)
	}

	orderType := 0
	switch req.OrderType {
	case order.ORDER_TYPE_MARKET:
		orderType = 10
	case order.ORDER_TYPE_LIMIT:
		orderType = 20
	}
	if orderType == 0 {
		return ord, fmt.Errorf("注文種別が不正です")
	}

	deliverType := 0
	switch ord.Action {
	case order.ACTION_BUY:
		if cashMargin == 1 {
			deliverType = 2
		}
	case order.ACTION_SELL:
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
	if len(closePositions) == 0 && req.ClosePositionOrder != order.CLOSE_POSITION_ORDER_NONE {
		val := int32(req.ClosePositionOrder)
		closePositionOrder = &val
	}

	kabReq := api.OrderRequest{
		Symbol:             ord.Symbol,
		Exchange:           m.toKabuExchageType(req.Exchange),
		SecurityType:       securityType,
		Side:               string(side),
		CashMargin:         cashMargin,
		MarginTradeType:    tradeType,
		AccountType:        AccountType,
		ExpireDay:          0,
		Qty:                ord.OrderQty,
		FrontOrderType:     int32(orderType),
		Price:              ord.OrderPrice,
		DelivType:          int32(deliverType),
		ClosePositionOrder: closePositionOrder,
		ClosePositions:     closePositions,
	}

	fmt.Printf("発注完了 %+v\n", kabReq)

	resp, err := m.client.SendOrder(kabReq)
	if err != nil {
		return ord, fmt.Errorf("カブコムAPI発注失敗: %w", err)
	}

	ord.ID = resp.OrderId
	ord.Status = order.ORDER_STATUS_WAITING
	ord.InternalState = order.STATE_ACTIVE // API送信成功・受付完了としてACTIVEへ遷移

	return ord, nil
}

// CancelOrder は market.MarketGateway (Orderer) の実装です
func (m *MarketGateway) CancelOrder(ctx context.Context, orderID string) error {
	resCh := m.dispatcher.Submit("", nil, nil, orderID)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case res := <-resCh:
		return res.Error
	}
}

// CancelOrderRaw は実際にAPIを叩く低レベルメソッドです
func (m *MarketGateway) CancelOrderRaw(ctx context.Context, orderID string) error {
	req := api.CancelRequest{OrderID: orderID}
	_, err := m.client.CancelOrder(req)
	if err != nil {
		return fmt.Errorf("キャンセル失敗 (OrderID: %s): %w", orderID, err)
	}
	return nil
}

func (m *MarketGateway) GetOrders(ctx context.Context) (order.Orders, error) {
	ords, err := m.client.GetOrders()
	if err != nil {
		return order.Orders{}, fmt.Errorf("注文取得失敗)")
	}

	domainOrders := make([]order.Order, 0, len(ords))
	for _, ord := range ords {
		action := order.ACTION_BUY
		if ord.Side == api.SIDE_SELL {
			action = order.ACTION_SELL
		}

		// api.Order.State を order.OrderStatus にマッピング
		// カブコム仕様: 1:待機, 2:処理中, 3:処理済, 4:訂正取消送信中, 5:終了
		status := order.ORDER_STATUS_WAITING
		switch ord.State {
		case api.STATE_WAITING, api.STATE_PROCESSING:
			status = order.ORDER_STATUS_WAITING
		case api.STATE_PROCESSED:
			status = order.ORDER_STATUS_IN_PROGRESS
		case api.STATE_CANCELING:
			status = order.ORDER_STATUS_CANCEL_SENT // 訂正取消送信中
		case api.STATE_FINISHED:
			// State:5 は最終状態。CumQty を見て全約定か一部約定・取消かを判断する
			if ord.CumQty >= ord.OrderQty && ord.OrderQty > 0 {
				status = order.ORDER_STATUS_FILLED
			} else {
				// 取消・失効・期限切れのいずれか
				// デフォルトを CANCELED とし、明細から詳細を判断
				status = order.ORDER_STATUS_CANCELED
				for _, detail := range ord.Details {
					if detail.RecType == api.RECTYPE_CANCELED { // 取消
						status = order.ORDER_STATUS_CANCELED
						break
					}
					if detail.RecType == api.RECTYPE_EXPIRED || detail.RecType == api.RECTYPE_INVALID { // 期限切れ・失効
						status = order.ORDER_STATUS_EXPIRED
						break
					}
				}
			}
		}

		o := order.NewOrder(ord.ID, ord.Symbol, action, ord.Price, ord.OrderQty)
		o.Status = status
		o.CumQty = ord.CumQty

		for _, execution := range ord.Details {
			// RecType が RECTYPE_EXECUTION (8: 約定) の場合のみ Execution として追加
			if execution.RecType != api.RECTYPE_EXECUTION || execution.ID == "" {
				continue
			}

			// 約定日時のパース
			execTime := parseExecutionTime(execution.ExecutionDay)

			o.AddExecution(
				order.Execution{
					ID:            execution.ID,
					Price:         execution.Price,
					Qty:           execution.Qty,
					ExecutionTime: execTime,
				},
			)
		}
		domainOrders = append(domainOrders, *o)
	}

	return order.Orders{Orders: domainOrders}, nil
}

func (m *MarketGateway) GetPositions(ctx context.Context, product order.ProductType) ([]position.Position, error) {
	arg := api.ProductMargin
	if product != order.PRODUCT_MARGIN {
		// 現状は信用取引しかしてない
		return nil, fmt.Errorf("prodcutが不正です %d", product)
	}
	positions, err := m.client.GetPositions(arg)
	if err != nil {
		return nil, fmt.Errorf("建玉取得失敗: %d)", product)
	}

	decodePositons := make([]position.Position, 0, len(positions))
	for _, pos := range positions {
		decodePositons = append(decodePositons, position.Position{
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

func (m *MarketGateway) toMakerAction(side string) order.Action {
	switch side {
	case string(api.SIDE_SELL):
		return order.ACTION_SELL
	case string(api.SIDE_BUY):
		return order.ACTION_BUY
	default:
		return ""
	}
}

func (m *MarketGateway) toMakerTradeType(tradeType int32) order.MarginTradeType {
	switch tradeType {
	case 1:
		return order.TRADE_TYPE_SYSTEM
	case 2:
		return order.TRADE_TYPE_GENERAL
	case 3:
		return order.TRADE_TYPE_GENERAL_DAY
	default:
		return order.TRADE_TYPE_NONE
	}
}

func (m *MarketGateway) toAccountType(accountType int32) order.AccountType {
	switch accountType {
	case 2:
		return order.ACCOUNT_GENERAL
	case 4:
		return order.ACCOUNT_SPECIAL
	case 12:
		return order.ACCOUNT_CORPORATE
	default:
		return order.ACCOUNT_NONE
	}
}

func (m *MarketGateway) startPollingLoop(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond) // 500ms間隔に短縮
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			ords, err := m.GetOrders(ctx)
			if err != nil {
				fmt.Printf("ポーリングエラー: %v\n", err)
				continue
			}

			// 注文一覧を全銘柄のチャネルへ安全にディスパッチ
			for symbol, ch := range m.orderChannels {
				select {
				case ch <- ords:
				default:
					fmt.Printf("⚠️ 🚨 [%s] OrdersReportチャネルがフルです。非同期でキューイングを試みます。\n", symbol)
					go func(c chan<- order.Orders, r order.Orders) {
						c <- r
					}(ch, ords)
				}
			}
		}
	}
}

func (s *MarketGateway) startWebSocketLoop(ctx context.Context) {
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
		// goroutine終了時（システム終了時など）にログファイルを閉じる
		defer logger.Close()
		for {
			select {
			case <-ctx.Done():
				// システム終了時は安全にゴルーチンを抜ける
				return
			case msg := <-rawCh:
				// 板情報を集約
				sellBoard := []tick.Quote{
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
				buyBoard := []tick.Quote{
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

				t := tick.NewTick(
					msg.Symbol,
					msg.CurrentPrice,
					msg.VWAP,
					msg.TradingVolume,
					msg.CurrentPriceTime,
					tick.FirstQuote{
						Price: msg.Sell1.Price,
						Qty:   msg.Sell1.Qty,
						Time:  msg.Sell1.Time,
						Sign:  msg.Sell1.Sign,
					},
					tick.FirstQuote{
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
				logger.Log(t)

				// 内部の DataPool を更新
				s.dataPool.PushTick(t)

				// 該当する銘柄のチャネルへルーティング (skip-on-full)
				if ch, ok := s.tickChannels[t.Symbol]; ok {
					select {
					case ch <- t:
					default:
						fmt.Printf("⚠️ [%s] ワーカー過負荷: Tickチャネルがフルのため、このTickを破棄(スキップ)します\n", t.Symbol)
					}
				}
			}
		}
	}()
}

func (m *MarketGateway) RegisterSymbol(ctx context.Context, req market.ResisterSymbolRequest) error {
	return m.RegisterSymbols(ctx, []market.ResisterSymbolRequest{req})
}

func (m *MarketGateway) RegisterSymbols(ctx context.Context, reqs []market.ResisterSymbolRequest) error {
	if len(reqs) == 0 {
		return nil
	}

	// 0. チャネルマップの初期化
	for _, req := range reqs {
		if _, exists := m.tickChannels[req.Symbol]; !exists {
			m.tickChannels[req.Symbol] = make(chan tick.Tick, 1000)
			m.orderChannels[req.Symbol] = make(chan order.Orders, 1000)
		}
	}

	// 50銘柄ずつバッチ処理
	const batchSize = 50
	for i := 0; i < len(reqs); i += batchSize {
		end := i + batchSize
		if end > len(reqs) {
			end = len(reqs)
		}

		batch := reqs[i:end]
		clientReq := api.RegisterSymbolRequest{
			Symbols: make([]api.RegisterSymbolsItem, 0, len(batch)),
		}

		for _, req := range batch {
			clientReq.Symbols = append(clientReq.Symbols, api.RegisterSymbolsItem{
				Symbol:   req.Symbol,
				Exchange: m.toBaseKabuExchageType(req.Exchange),
			})
		}

		_, err := m.client.RegisterSymbol(clientReq)
		if err != nil {
			return fmt.Errorf("銘柄一括登録失敗 (batch %d-%d): %w", i, end, err)
		}
		fmt.Printf("✅ 銘柄一括登録完了 (%d/%d): %d銘柄\n", end, len(reqs), len(batch))

		// レート制限（秒間上限）を考慮し、複数バッチある場合は少し待機
		if end < len(reqs) {
			time.Sleep(1 * time.Second)
		}
	}

	return nil
}

func (m *MarketGateway) GetSymbol(ctx context.Context, symbolCode string, exchange order.ExchangeMarket) (symbol.Symbol, error) {
	resp, err := m.client.GetSymbol(symbolCode, m.toBaseKabuExchageType(exchange))
	if err != nil {
		return symbol.Symbol{}, fmt.Errorf("銘柄情報取得失敗: %w", err)
	}

	prg, err := strconv.Atoi(resp.PriceRangeGroup)
	if err != nil {
		return symbol.Symbol{}, fmt.Errorf("PriceRangeGroupの数値変換失敗 (%s): %w", resp.PriceRangeGroup, err)
	}
	return symbol.Symbol{
		Code:            resp.Symbol,
		Name:            resp.SymbolName,
		PriceRangeGroup: symbol.PriceRangeGroup(prg),
	}, nil
}

func (m *MarketGateway) UnregisterSymbolAll(ctx context.Context) error {
	_, err := m.client.UnregisterSymbolAll()
	if err != nil {
		return fmt.Errorf("銘柄登録全解除失敗)")
	}
	return nil
}

func (m *MarketGateway) toMarketExchageType(exchange api.ExchageType) order.ExchangeMarket {
	switch exchange {
	case api.EXCHANGE_TYPE_TOSHO:
		return order.EXCHANGE_TOSHO
	case api.EXCHANGE_TYPE_TOSHO_PLS:
		return order.EXCHANGE_TOSHO_PLUS
	case api.EXCHANGE_TYPE_TOSHO_SOR:
		return order.EXCHANGE_SOR
	}
	return order.EXCHANGE_SOR
}

func (m *MarketGateway) toKabuExchageType(exchange order.ExchangeMarket) api.ExchageType {
	switch exchange {
	case order.EXCHANGE_TOSHO:
		return api.EXCHANGE_TYPE_TOSHO
	case order.EXCHANGE_TOSHO_PLUS:
		return api.EXCHANGE_TYPE_TOSHO_PLS
	case order.EXCHANGE_SOR:
		return api.EXCHANGE_TYPE_TOSHO_SOR
	}
	return api.EXCHANGE_TYPE_TOSHO_SOR
}

func (m *MarketGateway) toBaseKabuExchageType(exchange order.ExchangeMarket) api.ExchageType {
	switch exchange {
	case order.EXCHANGE_TOSHO, order.EXCHANGE_TOSHO_PLUS: // API仕様で東証+はない
		return api.EXCHANGE_TYPE_TOSHO
	case order.EXCHANGE_SOR:
		return api.EXCHANGE_TYPE_TOSHO_SOR
	}
	return api.EXCHANGE_TYPE_TOSHO_SOR
}
func (m *MarketGateway) toPriceStatus(kabuStatus int) tick.PriceStatus {
	// カブコム仕様: 1:現値, 3:寄付, 4:前引, 5:大引, 2:特別気配, 6:特成...
	// ドメイン仕様にマッピング
	switch kabuStatus {
	case 1:
		return tick.PRICE_STATUS_CURRENT
	case 3:
		return tick.PRICE_STATUS_OPENING
	case 4:
		return tick.PRICE_STATUS_PRE_CLOSE
	case 5:
		return tick.PRICE_STATUS_CLOSE
	case 2, 6, 7, 8, 9: // 特別気配系
		return tick.PRICE_STATUS_SPECIAL
	default:
		return tick.PRICE_STATUS_NONE
	}
}

func (m *MarketGateway) toPriceChangeStatus(kabuStatus string) tick.PriceChangeStatus {
	// カブコム仕様: '0000': 変わらず, '0056': 上昇, '0057': 下落...
	switch kabuStatus {
	case "0056":
		return tick.PRICE_CHANGE_UP
	case "0057":
		return tick.PRICE_CHANGE_DOWN
	case "0000":
		return tick.PRICE_CHANGE_UNCHANGED
	default:
		return tick.PRICE_CHANGE_NONE
	}
}

// parseExecutionTime はカブコムAPIが返却する多様な約定日時の文字列フォーマットに対応し、
// 正確に time.Time へ変換するための堅牢なヘルパー関数です。
func parseExecutionTime(timeStr string) time.Time {
	if timeStr == "" {
		return time.Time{}
	}

	// 1. RFC3339 形式 (例: "2020-10-23T11:21:40+09:00" や "2026-05-18T09:01:33.456+09:00")
	if t, err := time.Parse(time.RFC3339, timeStr); err == nil {
		return t
	}

	// 2. 日本国内の金融APIやシステムで頻出する各種フォーマット
	formats := []string{
		"2006/01/02 15:04:05.000",
		"2006/01/02 15:04:05",
		"2006-01-02 15:04:05.000",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"20060102150405",
	}

	// タイムゾーンが明示されない場合は、カブコムAPIの仕様に従い日本時間 (Asia/Tokyo) としてパース
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		loc = time.FixedZone("Asia/Tokyo", 9*60*60)
	}

	for _, fmtStr := range formats {
		if t, err := time.ParseInLocation(fmtStr, timeStr, loc); err == nil {
			return t
		}
	}

	return time.Time{}
}

// KabuHistoricalFeederProvider は KabuClient を利用する HistoricalFeederProvider です
type KabuHistoricalFeederProvider struct {
	client KabuClientInterface
}

var _ tick.HistoricalFeederProvider = (*KabuHistoricalFeederProvider)(nil)

func NewKabuHistoricalFeederProvider(client KabuClientInterface) *KabuHistoricalFeederProvider {
	return &KabuHistoricalFeederProvider{
		client: client,
	}
}

func (p *KabuHistoricalFeederProvider) GetFeeder(symbol string) tick.HistoricalFeeder {
	return &KabuHistoricalFeeder{
		symbol: symbol,
		client: p.client,
	}
}

// KabuHistoricalFeeder は前日終値の取得にカブコムの REST API (/board) を利用します
type KabuHistoricalFeeder struct {
	symbol string
	client KabuClientInterface
}

var _ tick.HistoricalFeeder = (*KabuHistoricalFeeder)(nil)

func (f *KabuHistoricalFeeder) FetchSMA(period int) (float64, error) {
	return 0, fmt.Errorf("SMA is not supported by KabuHistoricalFeeder")
}

func (f *KabuHistoricalFeeder) FetchPreviousClose() (float64, error) {
	slog.Info("Fetching previous close from KabuStation API", slog.String("symbol", f.symbol))
	board, err := f.client.GetBoard(f.symbol)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch previous close from KabuStation API: %w", err)
	}
	if board == nil {
		return 0, fmt.Errorf("failed to fetch previous close: board response is empty")
	}
	if board.PreviousClose <= 0 {
		return 0, fmt.Errorf("failed to fetch previous close: PreviousClose returned by KabuStation is invalid: %.2f", board.PreviousClose)
	}

	slog.Info("Successfully fetched previous close from KabuStation API", slog.String("symbol", f.symbol), slog.Float64("previous_close", board.PreviousClose))
	return board.PreviousClose, nil
}
