package kabu

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
	"github.com/r-umemoto/trading-bot/pkg/infra/kabu/api"
)

// MockKabuClient は KabuClient の振る舞いを模倣します
type MockKabuClient struct {
	Orders          []api.Order
	LastSendRequest *api.OrderRequest
	GetTokenCount   int
	RegisterCount   int
	GetBoardCount   int
	GetBoardFunc    func(symbol string) (*api.BoardResponse, error)
}

func (m *MockKabuClient) GetToken() error {
	m.GetTokenCount++
	return nil
}

func (m *MockKabuClient) GetOrders() ([]api.Order, error) {
	return m.Orders, nil
}

func (m *MockKabuClient) SendOrder(req api.OrderRequest) (*api.OrderResponse, error) {
	m.LastSendRequest = &req
	return &api.OrderResponse{OrderId: "test-order-id"}, nil
}
func (m *MockKabuClient) CancelOrder(req api.CancelRequest) (*api.CancelResponse, error) {
	return nil, nil
}
func (m *MockKabuClient) GetPositions(product api.ProductType) ([]api.Position, error) {
	return nil, nil
}
func (m *MockKabuClient) RegisterSymbol(req api.RegisterSymbolRequest) (*api.RegisterSymbolResponse, error) {
	m.RegisterCount++
	return nil, nil
}
func (m *MockKabuClient) UnregisterSymbolAll() (*api.UnregisterSymbolAllResponse, error) {
	return nil, nil
}
func (m *MockKabuClient) GetSymbol(symbol string, exchange api.ExchageType) (*api.SymbolSuccess, error) {
	return nil, nil
}
func (m *MockKabuClient) GetBoard(symbol string) (*api.BoardResponse, error) {
	m.GetBoardCount++
	if m.GetBoardFunc != nil {
		return m.GetBoardFunc(symbol)
	}
	return &api.BoardResponse{Symbol: symbol, CurrentPrice: 5000.0}, nil
}

func TestMarketGateway_GetOrders(t *testing.T) {
	mockClient := &MockKabuClient{}
	gateway := &MarketGateway{
		client: mockClient,
	}

	// テストケース: 様々な API レスポンスのパターン
	tests := []struct {
		name     string
		apiOrder api.Order
		expected order.OrderStatus
	}{
		{
			name: "State 4: Canceling",
			apiOrder: api.Order{
				ID:    "order-1",
				State: api.STATE_CANCELING,
			},
			expected: order.ORDER_STATUS_CANCEL_SENT,
		},
		{
			name: "State 5 with RecType 6: Canceled",
			apiOrder: api.Order{
				ID:    "order-2",
				State: api.STATE_FINISHED,
				Details: []api.OrderDetail{
					{RecType: api.RECTYPE_CANCELED},
				},
			},
			expected: order.ORDER_STATUS_CANCELED,
		},
		{
			name: "State 5 with Full Fill",
			apiOrder: api.Order{
				ID:       "order-3",
				State:    api.STATE_FINISHED,
				OrderQty: 100,
				CumQty:   100,
			},
			expected: order.ORDER_STATUS_FILLED,
		},
		{
			name: "State 5 with RecType 7: Expired",
			apiOrder: api.Order{
				ID:    "order-4",
				State: api.STATE_FINISHED,
				Details: []api.OrderDetail{
					{RecType: api.RECTYPE_INVALID},
				},
			},
			expected: order.ORDER_STATUS_EXPIRED,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient.Orders = []api.Order{tt.apiOrder}
			ords, err := gateway.GetOrders(context.Background())
			if err != nil {
				t.Fatalf("GetOrders failed: %v", err)
			}

			if len(ords.Orders) != 1 {
				t.Fatalf("expected 1 order, got %d", len(ords.Orders))
			}

			if ords.Orders[0].Status() != tt.expected {
				t.Errorf("expected status %d, got %d", tt.expected, ords.Orders[0].Status())
			}
		})
	}
}

func TestMarketGateway_GetOrders_CreatedAtParsing(t *testing.T) {
	mockClient := &MockKabuClient{}
	gateway := &MarketGateway{
		client: mockClient,
	}

	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		loc = time.FixedZone("Asia/Tokyo", 9*60*60)
	}
	todayStr := time.Now().In(loc).Format("20060102")
	yesterdayStr := time.Now().In(loc).AddDate(0, 0, -1).Format("20060102")
	recvTimeStr := time.Now().In(loc).Format("2006-01-02T15:04:05.123456-07:00")

	mockClient.Orders = []api.Order{
		{
			ID:       todayStr + "0001",
			State:    api.STATE_PROCESSING,
			RecvTime: recvTimeStr,
		},
		{
			ID:    todayStr + "0002", // Test ID prefix fallback when RecvTime is empty
			State: api.STATE_PROCESSING,
		},
		{
			ID:    yesterdayStr + "0003", // Test multi-day order retrieval and fallback parsing
			State: api.STATE_PROCESSING,
		},
	}

	ords, err := gateway.GetOrders(context.Background())
	if err != nil {
		t.Fatalf("GetOrders failed: %v", err)
	}

	if len(ords.Orders) != 3 {
		t.Fatalf("expected exactly 3 orders, got %d", len(ords.Orders))
	}

	// Verify order 1 has CreatedAt parsed from RecvTime
	expectedRecvTime, _ := time.Parse(time.RFC3339, recvTimeStr)
	if !ords.Orders[0].CreatedAt.Equal(expectedRecvTime) {
		t.Errorf("expected CreatedAt to match RecvTime %v, got %v", expectedRecvTime, ords.Orders[0].CreatedAt)
	}

	// Verify order 2 has CreatedAt fallback from ID prefix
	expectedFallbackDate, _ := time.ParseInLocation("20060102", todayStr, loc)
	if !ords.Orders[1].CreatedAt.Equal(expectedFallbackDate) {
		t.Errorf("expected CreatedAt to match ID prefix date %v, got %v", expectedFallbackDate, ords.Orders[1].CreatedAt)
	}

	// Verify order 3 (yesterday's order) is retrieved and has CreatedAt fallback from yesterday ID prefix
	expectedYesterdayFallback, _ := time.ParseInLocation("20060102", yesterdayStr, loc)
	if !ords.Orders[2].CreatedAt.Equal(expectedYesterdayFallback) {
		t.Errorf("expected CreatedAt to match yesterday ID prefix date %v, got %v", expectedYesterdayFallback, ords.Orders[2].CreatedAt)
	}
}

func TestMarketGateway_SendOrderRaw_DelivType(t *testing.T) {
	mockClient := &MockKabuClient{}
	gateway := &MarketGateway{
		client: mockClient,
	}

	tests := []struct {
		name             string
		cashMargin       order.CashMarginType
		action           order.Action
		closePosOrder    order.ClosePositionOrder
		closePos         []order.ClosePosition
		expectedDelivType int32
	}{
		{
			name:             "現物買 -> DelivType=2",
			cashMargin:       order.CASH_MARGIN_CASH,
			action:           order.ACTION_BUY,
			expectedDelivType: 2,
		},
		{
			name:             "現物売 -> DelivType=0",
			cashMargin:       order.CASH_MARGIN_CASH,
			action:           order.ACTION_SELL,
			expectedDelivType: 0,
		},
		{
			name:             "信用新規 -> DelivType=0",
			cashMargin:       order.CASH_MARGIN_MARGIN_ENTRY,
			action:           order.ACTION_BUY,
			expectedDelivType: 0,
		},
		{
			name:             "信用返済 (ClosePositionOrder指定) -> DelivType=2",
			cashMargin:       order.CASH_MARGIN_MARGIN_EXIT,
			action:           order.ACTION_SELL,
			closePosOrder:    order.CLOSE_POSITION_ASC_DAY_DEC_PL,
			expectedDelivType: 2,
		},
		{
			name:             "信用返済 (ClosePositions指定) -> DelivType=2",
			cashMargin:       order.CASH_MARGIN_MARGIN_EXIT,
			action:           order.ACTION_SELL,
			closePos:         []order.ClosePosition{{HoldID: "test-id", Qty: 100}},
			expectedDelivType: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ord := order.NewOrder("test-local-id", "8801", tt.action, 1000, 100)
			ord.CashMargin = tt.cashMargin

			ord.Request = &order.OrderRequest{
				Exchange:           order.EXCHANGE_TOSHO,
				SecurityType:       order.SECURITY_TYPE_STOCK,
				MarginTradeType:    order.TRADE_TYPE_SYSTEM,
				AccountType:        order.ACCOUNT_SPECIAL,
				ClosePositionOrder: tt.closePosOrder,
				ClosePositions:     tt.closePos,
			}
			ord.Type = order.ORDER_TYPE_LIMIT

			_, err := gateway.SendOrderRaw(context.Background(), order.SendOrderInput{
				Order:   ord,
			})

			if err != nil {
				t.Fatalf("SendOrderRaw failed: %v", err)
			}

			if mockClient.LastSendRequest == nil {
				t.Fatal("API request was not captured by mock client")
			}

			if mockClient.LastSendRequest.DelivType != tt.expectedDelivType {
				t.Errorf("expected DelivType %d, got %d", tt.expectedDelivType, mockClient.LastSendRequest.DelivType)
			}
		})
	}
}

func TestMarketGateway_StartWebSocketLoop(t *testing.T) {
	// 1. WebSocketサーバの起動
	upgrader := websocket.Upgrader{}
	wsCh := make(chan string, 10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// クライアントからの切断指示や接続終了を待つループ
		for {
			select {
			case msg, ok := <-wsCh:
				if !ok {
					return
				}
				_ = conn.WriteMessage(websocket.TextMessage, []byte(msg))
			default:
				time.Sleep(10 * time.Millisecond)
			}
		}
	}))
	defer server.Close()
	defer close(wsCh)

	// 2. Gatewayの準備
	mockClient := &MockKabuClient{}
	wsURL := strings.Replace(server.URL, "http://", "ws://", 1) + "/websocket"
	wsClient := api.NewWSClient(wsURL)

	gateway := NewMarketGateway(nil, wsClient)
	gateway.client = mockClient

	// 監視対象としてあらかじめ銘柄登録しておく
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := gateway.RegisterSymbols(ctx, []market.ResisterSymbolRequest{
		{Symbol: "7201", Exchange: order.EXCHANGE_TOSHO},
	})
	if err != nil {
		t.Fatalf("RegisterSymbols failed: %v", err)
	}

	// startWebSocketLoop を非同期起動
	gateway.startWebSocketLoop(ctx)

	// 3. 通常の WebSocket 受信テスト
	// ダミー価格データをプッシュ
	dummyMsg := `{"Symbol":"7201","CurrentPrice":3990.0,"CurrentPriceTime":"2026-06-01T09:00:00+09:00"}`
	wsCh <- dummyMsg

	// DataPool が更新されるのを待つ
	tickCh := gateway.tickChannels["7201"]
	select {
	case tk := <-tickCh:
		if tk.Symbol != "7201" || tk.Price != 3990.0 {
			t.Errorf("Unexpected tick received: %+v", tk)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Timeout waiting for WebSocket tick")
	}

	// 4. DataPool の検証
	var state tick.MarketState = gateway.DataPool().GetState("7201")
	if state.LatestTick.Price != 3990.0 {
		t.Errorf("Expected LatestTick Price to be 3990.0, got %f", state.LatestTick.Price)
	}
}

func TestMarketGateway_CheckAndFireIFD_PartialFill(t *testing.T) {
	mockClient := &MockKabuClient{}
	gateway := NewMarketGateway(nil, nil)
	gateway.client = mockClient

	// 親注文と子注文テンプレートの作成
	parentID := "parent-1"
	parent := order.NewOrder(parentID, "7203", order.ACTION_BUY, 2000, 100)
	
	childTemplate := order.NewOrder("child-local-id", "7203", order.ACTION_SELL, 2005, 100)
	childTemplate.Request = &order.OrderRequest{
		Exchange:        order.EXCHANGE_TOSHO,
		SecurityType:    order.SECURITY_TYPE_STOCK,
		MarginTradeType: order.TRADE_TYPE_GENERAL_DAY,
		AccountType:     order.ACCOUNT_SPECIAL,
	}

	gateway.ifdTracker[parentID] = childTemplate

	// 1. 部分約定 (30株) の発生
	exec1 := order.Execution{
		ID:            "exec-1",
		Price:         2000,
		Qty:           30,
		ExecutionTime: time.Now(),
	}
	parent.AddExecution(exec1)
	parent.CumQty = 30
	parent.BypassTransition(order.ORDER_STATUS_IN_PROGRESS, order.STATE_ACTIVE)

	report := order.Orders{
		Orders: []order.Order{*parent},
	}

	gateway.checkAndFireIFD(context.Background(), report)

	// 非同期の Submit を少し待つ
	time.Sleep(10 * time.Millisecond)

	// Dispatcher から送信されたジョブを取得して検証
	job := gateway.dispatcher.pickBestJob()
	if job == nil {
		t.Fatal("expected a child order job to be submitted to dispatcher")
	}

	if job.OrderPtr == nil {
		t.Fatal("expected order pointer in job")
	}

	if job.OrderPtr.OrderQty != 30 {
		t.Errorf("expected child order qty 30, got %f", job.OrderPtr.OrderQty)
	}

	if len(job.OrderPtr.Request.ClosePositions) != 1 {
		t.Fatalf("expected 1 close position, got %d", len(job.OrderPtr.Request.ClosePositions))
	}

	if job.OrderPtr.Request.ClosePositions[0].HoldID != "exec-1" {
		t.Errorf("expected HoldID exec-1, got %s", job.OrderPtr.Request.ClosePositions[0].HoldID)
	}

	if !gateway.firedExecutions["exec-1"] {
		t.Error("expected exec-1 to be marked as fired")
	}

	// 2. 重複チェック（再度 checkAndFireIFD を呼んでも exec-1 に対して重複発注されないこと）
	gateway.checkAndFireIFD(context.Background(), report)
	
	time.Sleep(10 * time.Millisecond)
	
	job2 := gateway.dispatcher.pickBestJob()
	if job2 != nil {
		t.Error("expected no duplicate order job to be submitted")
	}

	// 3. 追加の部分約定 (50株) の発生
	exec2 := order.Execution{
		ID:            "exec-2",
		Price:         2000,
		Qty:           50,
		ExecutionTime: time.Now(),
	}
	parent.AddExecution(exec2)
	parent.CumQty = 80

	report2 := order.Orders{
		Orders: []order.Order{*parent},
	}

	gateway.checkAndFireIFD(context.Background(), report2)

	time.Sleep(10 * time.Millisecond)

	job3 := gateway.dispatcher.pickBestJob()
	if job3 == nil {
		t.Fatal("expected a child order job for exec-2 to be submitted")
	}

	if job3.OrderPtr.OrderQty != 50 {
		t.Errorf("expected child order qty 50, got %f", job3.OrderPtr.OrderQty)
	}

	if job3.OrderPtr.Request.ClosePositions[0].HoldID != "exec-2" {
		t.Errorf("expected HoldID exec-2, got %s", job3.OrderPtr.Request.ClosePositions[0].HoldID)
	}
}

func TestKabuHistoricalFeeder_FetchPreviousClose_WritesCSV(t *testing.T) {
	tempDir := t.TempDir()
	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get wd: %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer os.Chdir(origCwd)

	mockClient := &MockKabuClient{
		GetBoardFunc: func(symbol string) (*api.BoardResponse, error) {
			return &api.BoardResponse{
				Symbol:        symbol,
				PreviousClose: 1234.5,
			}, nil
		},
	}

	feeder := &KabuHistoricalFeeder{
		symbol: "8604",
		client: mockClient,
	}

	val, err := feeder.FetchPreviousClose()
	if err != nil {
		t.Fatalf("FetchPreviousClose failed: %v", err)
	}
	if val != 1234.5 {
		t.Errorf("expected 1234.5, got %f", val)
	}

	// Verify that ./data/<today>/closes.csv was written
	today := time.Now().Format("20060102")
	csvPath := filepath.Join("data", today, "closes.csv")
	if _, err := os.Stat(csvPath); err != nil {
		t.Fatalf("expected closes.csv to be created at %s, but got error: %v", csvPath, err)
	}

	// Read and verify contents
	content, err := os.ReadFile(csvPath)
	if err != nil {
		t.Fatalf("failed to read closes.csv: %v", err)
	}

	expectedContent := "Symbol,PreviousClose\n8604,1234.5\n"
	if string(content) != expectedContent {
		t.Errorf("expected content:\n%q\ngot:\n%q", expectedContent, string(content))
	}

	// Fetch again to verify that it doesn't duplicate
	_, err = feeder.FetchPreviousClose()
	if err != nil {
		t.Fatalf("second FetchPreviousClose failed: %v", err)
	}

	content2, err := os.ReadFile(csvPath)
	if err != nil {
		t.Fatalf("failed to read closes.csv second time: %v", err)
	}
	if string(content2) != expectedContent {
		t.Errorf("expected content to remain unchanged, but got:\n%q", string(content2))
	}
}

type regulatedMockClient struct {
	MockKabuClient
	sendOrderFunc func(req api.OrderRequest) (*api.OrderResponse, error)
}

func (m *regulatedMockClient) SendOrder(req api.OrderRequest) (*api.OrderResponse, error) {
	if m.sendOrderFunc != nil {
		return m.sendOrderFunc(req)
	}
	return m.MockKabuClient.SendOrder(req)
}

func TestMarketGateway_ShortRegulation(t *testing.T) {
	mockClient := &regulatedMockClient{}
	gateway := NewMarketGateway(nil, nil)
	gateway.client = mockClient

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gateway.dispatcher.Start(ctx)

	// 1. First order fails with 100302
	mockClient.sendOrderFunc = func(req api.OrderRequest) (*api.OrderResponse, error) {
		return nil, &api.KabuAPIError{
			StatusCode: 400,
			Code:       100302,
			Message:    "売建規制エラーです",
		}
	}

	ord := order.NewOrder("order-1", "7203", order.ACTION_SELL, 2000, 100)
	ord.CashMargin = order.CASH_MARGIN_MARGIN_ENTRY
	ord.Request = &order.OrderRequest{
		Exchange:        order.EXCHANGE_TOSHO,
		SecurityType:    order.SECURITY_TYPE_STOCK,
		MarginTradeType: order.TRADE_TYPE_GENERAL_DAY,
		AccountType:     order.ACCOUNT_SPECIAL,
	}

	_, err := gateway.SendOrder(context.Background(), order.SendOrderInput{Order: ord})
	if err == nil || !errors.Is(err, order.ErrShortRegulated) {
		t.Fatalf("expected ErrShortRegulated error, got %v", err)
	}

	// Verify symbol is marked as short-disabled
	gateway.shortDisabledMu.RLock()
	until, ok := gateway.shortDisabledUntil["7203"]
	gateway.shortDisabledMu.RUnlock()
	if !ok || time.Now().After(until) {
		t.Error("expected 7203 to be marked as short disabled until some future time")
	}

	// 2. Subsequent short entry order should be blocked locally
	ord2 := order.NewOrder("order-2", "7203", order.ACTION_SELL, 2000, 100)
	ord2.CashMargin = order.CASH_MARGIN_MARGIN_ENTRY
	ord2.Request = ord.Request

	_, err = gateway.SendOrder(context.Background(), order.SendOrderInput{Order: ord2})
	if err == nil || !errors.Is(err, order.ErrOrderSkipped) {
		t.Fatalf("expected ErrOrderSkipped error, got %v", err)
	}

	// 3. Long entry order should still be allowed
	mockClient.sendOrderFunc = func(req api.OrderRequest) (*api.OrderResponse, error) {
		return &api.OrderResponse{OrderId: "long-order-id"}, nil
	}
	ordLong := order.NewOrder("order-long", "7203", order.ACTION_BUY, 2000, 100)
	ordLong.CashMargin = order.CASH_MARGIN_MARGIN_ENTRY
	ordLong.Request = ord.Request

	res, err := gateway.SendOrder(context.Background(), order.SendOrderInput{Order: ordLong})
	if err != nil {
		t.Fatalf("expected long entry to be allowed, got error %v", err)
	}
	if res.ID != "long-order-id" {
		t.Errorf("expected order ID 'long-order-id', got '%s'", res.ID)
	}

	// 4. Exit/Close short order should still be allowed
	mockClient.sendOrderFunc = func(req api.OrderRequest) (*api.OrderResponse, error) {
		return &api.OrderResponse{OrderId: "exit-order-id"}, nil
	}
	ordExit := order.NewOrder("order-exit", "7203", order.ACTION_BUY, 2000, 100)
	ordExit.CashMargin = order.CASH_MARGIN_MARGIN_EXIT
	ordExit.Request = &order.OrderRequest{
		Exchange:        order.EXCHANGE_TOSHO,
		SecurityType:    order.SECURITY_TYPE_STOCK,
		MarginTradeType: order.TRADE_TYPE_GENERAL_DAY,
		AccountType:     order.ACCOUNT_SPECIAL,
		ClosePositions:  []order.ClosePosition{{HoldID: "exec-1", Qty: 100}},
	}

	resExit, err := gateway.SendOrder(context.Background(), order.SendOrderInput{Order: ordExit})
	if err != nil {
		t.Fatalf("expected exit/close order to be allowed, got error %v", err)
	}
	if resExit.ID != "exit-order-id" {
		t.Errorf("expected order ID 'exit-order-id', got '%s'", resExit.ID)
	}
}

