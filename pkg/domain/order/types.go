package order

import (
	"encoding/json"
	"strings"
)

type Action string

const (
	ACTION_BUY  Action = "BUY"
	ACTION_SELL Action = "SELL"
)

func (a Action) ToMarketAction() (Action, bool) {
	switch a {
	case ACTION_BUY, ACTION_SELL:
		return a, true
	default:
		return "", false
	}
}

type ProductType int

const (
	PRODICT_NONE   ProductType = iota
	PRODICT_CASH               // 現物
	PRODUCT_MARGIN             // 信用
)

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

// OrderResult はインフラ層での発注・キャンセル結果をドメイン層へ通知するための構造体です
type OrderResult struct {
	Symbol  string
	OrderID string // APIから返されたOrderID（成功時）
	Order   *Order // 元の注文情報
	Error   error  // 失敗時のエラー内容
}
