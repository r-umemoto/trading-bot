package market

import (
	"context"
	"encoding/json"
	"strings"
)

type Action string

const (
	ACTION_BUY  Action = "BUY"
	ACTION_SELL Action = "SELL"
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
	EXCHANGE_NONE       ExchangeMarket = iota
	EXCHANGE_TOSHO                     // 東証
	EXCHANGE_SOR                       // SOR
	EXCHANGE_TOSHO_PLUS                // 東証
)

func (e ExchangeMarket) String() string {
	switch e {
	case EXCHANGE_TOSHO:
		return "TOSHO"
	case EXCHANGE_SOR:
		return "SOR"
	case EXCHANGE_TOSHO_PLUS:
		return "TOSHO_PLUS"
	default:
		return "NONE"
	}
}

func (e ExchangeMarket) MarshalJSON() ([]byte, error) {
	return json.Marshal(e.String())
}

func (e *ExchangeMarket) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		// フォールバック: もし既存の数値形式(3など)が含まれていた場合のため
		var i int
		if err2 := json.Unmarshal(data, &i); err2 == nil {
			*e = ExchangeMarket(i)
			return nil
		}
		return err
	}

	switch strings.ToUpper(s) {
	case "TOSHO":
		*e = EXCHANGE_TOSHO
	case "SOR":
		*e = EXCHANGE_SOR
	case "TOSHO_PLUS":
		*e = EXCHANGE_TOSHO_PLUS
	default:
		*e = EXCHANGE_NONE
	}
	return nil
}

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

type ClosePosition struct {
	HoldID string  // 返済対象の建玉（約定）ID
	Qty    float64 // 返済数量
}

type OrderRequest struct {
	Symbol             string
	Exchange           ExchangeMarket
	SecurityType       SecurityType
	Action             Action
	MarginTradeType    MarginTradeType
	AccountType        AccountType
	ClosePositionOrder ClosePositionOrder
	ClosePositions     []ClosePosition // 指定返済用
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

type ResisterSymbolRequest struct {
	Symbol   string
	Exchange ExchangeMarket
}

// MarketGateway は市場への統合アクセスポイントです
type MarketGateway interface {
	// Start は市場との接続を開始し、リアルタイム情報の受信を開始します
	Start(ctx context.Context) (<-chan Tick, <-chan ExecutionReport, error)

	SendOrder(ctx context.Context, req OrderRequest) (string, error) // 戻り値は受付OrderID
	CancelOrder(ctx context.Context, orderID string) error
	GetPositions(ctx context.Context, product ProductType) ([]Position, error)
	GetOrders(ctx context.Context) ([]Order, error)
	RegisterSymbol(ctx context.Context, req ResisterSymbolRequest) error
	UnregisterSymbolAll(ctx context.Context) error
}
