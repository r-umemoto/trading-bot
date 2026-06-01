package brain

import (
	"fmt"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
)

type Action string

const (
	ACTION_BUY  Action = "BUY"
	ACTION_SELL Action = "SELL"
	ACTION_HOLD Action = "HOLD"
)

type TradeType int

const (
	TradeEntry TradeType = iota // 新規建て
	TradeExit                   // 返済決済
)

// Signal は戦略がスナイパーに返す「命令」です
type Signal struct {
	Action    Action
	TradeType TradeType // 🌟 新規か決済か
	Quantity  float64
	Price     float64
	OrderType order.OrderType
	Reason    string // 🌟 命令の理由 (分析用)
}

func (a Action) ToMarketAction() (order.Action, error) {
	switch a {
	case ACTION_BUY:
		return order.ACTION_BUY, nil
	case ACTION_SELL:
		return order.ACTION_SELL, nil
	}
	return "", fmt.Errorf("変換できないアクションタイプ")
}

// ActionInput はシグナル生成のための入力パラメータです
type ActionInput struct {
	Price     float64         // 戦略が決定した注文価格
	Quantity  float64         // 戦略が決定した注文数量
	OrderType order.OrderType // 注文種別 (指値・成行)
	Reason    string          // 🌟 理由 (分析用)
}

// ActionBuilder は具体的なSignalを生成するインターフェースです
type ActionBuilder interface {
	BuildSignal(input ActionInput) Signal
}

// NewBuyEntry は「新規買い」のSignalを組み立てます
func NewBuyEntry(qty, price float64, orderType order.OrderType, reason string) Signal {
	if orderType == 0 {
		orderType = order.ORDER_TYPE_LIMIT
	}
	return Signal{
		Action:    ACTION_BUY,
		TradeType: TradeEntry,
		Quantity:  qty,
		Price:     price,
		OrderType: orderType,
		Reason:    reason,
	}
}

// NewSellEntry は「新規空売り」のSignalを組み立てます
func NewSellEntry(qty, price float64, orderType order.OrderType, reason string) Signal {
	if orderType == 0 {
		orderType = order.ORDER_TYPE_LIMIT
	}
	return Signal{
		Action:    ACTION_SELL,
		TradeType: TradeEntry,
		Quantity:  qty,
		Price:     price,
		OrderType: orderType,
		Reason:    reason,
	}
}

// NewBuyExit は「空売り返済の買い戻し」のSignalを組み立てます
func NewBuyExit(qty, price float64, orderType order.OrderType, reason string) Signal {
	if orderType == 0 {
		orderType = order.ORDER_TYPE_LIMIT
	}
	return Signal{
		Action:    ACTION_BUY,
		TradeType: TradeExit,
		Quantity:  qty,
		Price:     price,
		OrderType: orderType,
		Reason:    reason,
	}
}

// NewSellExit は「ロング建玉の転売返済」のSignalを組み立てます
func NewSellExit(qty, price float64, orderType order.OrderType, reason string) Signal {
	if orderType == 0 {
		orderType = order.ORDER_TYPE_LIMIT
	}
	return Signal{
		Action:    ACTION_SELL,
		TradeType: TradeExit,
		Quantity:  qty,
		Price:     price,
		OrderType: orderType,
		Reason:    reason,
	}
}

// NewHold は「取引見送り」のSignalを組み立てます
func NewHold() Signal {
	return Signal{
		Action: ACTION_HOLD,
	}
}

