package market

import (
	"context"
)

// Tick はシステム共通の価格データ（カブコムの仕様を一切知らない純粋なデータ）
type Tick struct {
	Symbol string
	Price  float64
	VWAP   float64
}

type Action string

const (
	Buy  Action = "BUY"
	Sell Action = "SELL"
)

type ProductType int

const (
	PRODICT_NONE   ProductType = iota
	PRODICT_CASH               // 現物
	PRODUCT_MARGIN             // 信用
)

// ExecutionReport は市場で発生した約定の事実を表します
type ExecutionReport struct {
	OrderID     string  // 紐づく注文ID
	ExecutionID string  // 約定のID
	Symbol      string  // 銘柄
	Action      Action  // 買いか売りか
	Price       float64 // 実際の約定単価
	Qty         float64 // 実際に約定した数量
}

// EventStreamer は、市場で発生するあらゆるイベントを受信するための規格です
type EventStreamer interface {
	// Start は市場との接続を開始し、2つのイベントチャネルを返します
	Start(ctx context.Context) (<-chan Tick, <-chan ExecutionReport, error)
}

type OrderType uint32

const (
	ORDER_TYPE_MARKET OrderType = 10
	ORDER_TYPE_LIMIT  OrderType = 20
)

type AccountType uint32

const (
	ACCOUNT_NONE      AccountType = iota
	ACCOUNT_GENERAL               // 一般
	ACCOUNT_SPECIAL               // 特定
	ACCOUNT_CORPORATE             // 法人
)

type ExchangeMarket uint32

const (
	EXCHANGE_NONE  ExchangeMarket = iota
	EXCHANGE_TOSHO                // 東証
	EXCHANGE_SOR                  // SOR
)

// これ間違えると手数料かかってくるから注意
type MarginTradeType uint32

const (
	TRADE_TYPE_NONE        MarginTradeType = iota
	TRADE_TYPE_SYSTEM                      // 制度信用
	TRADE_TYPE_GENERAL                     // 一般信用長期
	TRADE_TYPE_GENERAL_DAY                 // 一般信用デイトレ
)

type SecurityType uint32

const (
	SECURITY_TYPE_NONE SecurityType = iota
	SECURITY_TYPE_STOCK
)

type ClosePositionOrder uint32

const (
	CLOSE_POSITION_ORDER_NONE     ClosePositionOrder = iota
	CLOSE_POSITION_ASC_DAY_DEC_PL                    // 日付（古い順）、損益（高い順）
)

type OrderRequest struct {
	Symbol             string
	Exchange           ExchangeMarket
	SecurityType       SecurityType
	Action             Action
	MarginTradeType    MarginTradeType
	AccountType        AccountType
	ClosePositionOrder ClosePositionOrder
	OrderType          OrderType
	Qty                float64
	Price              float64
}

// OrderRequest は市場へ送る注文の要望です
type Position struct {
	ExecutionID string
	Symbol      string // 銘柄
	Exchange    ExchangeMarket
	Action      Action
	TradeType   MarginTradeType
	AccountType AccountType
	LeavesQty   float64 // 保有数量
	Price       float64 // 取得価格
}

type OrderState uint32

const (
	WAITING     OrderState = 1
	PROCCESSING OrderState = 2
	PROCCESSED  OrderState = 3
	FINISH      OrderState = 4
)

type Side string

const (
	SideBuy  Side = "2"
	SideSell Side = "1"
)

// OrderBroker は市場へ注文を仲介する規格です（インフラ層で実装します）
type OrderBroker interface {
	SendOrder(ctx context.Context, req OrderRequest) (string, error) // 戻り値は受付OrderID
	CancelOrder(ctx context.Context, orderID string) error
	GetPositions(ctx context.Context, product ProductType) ([]Position, error)
	GetOrders(ctx context.Context) ([]Order, error)
}
