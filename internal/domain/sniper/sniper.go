package sniper

import (
	"fmt"
	"sync"

	"trading-bot/internal/domain/market"
	"trading-bot/internal/domain/sniper/brain"
	"trading-bot/internal/domain/sniper/strategy"
)

// すべての戦略が満たすべき頭脳の規格
type Strategy interface {
	Evaluate(input strategy.StrategyInput) brain.Signal
}

// ★ スナイパー内で定義する「オプショナルな機能」の規格
type KillSwitchable interface {
	Activate() brain.Signal
}

// Sniper は戦略とAPIクライアントを持ち、執行を担います
type Sniper struct {
	Symbol          string
	positions       []market.Position
	Strategy        Strategy
	Orders          []*market.Order
	mu              sync.Mutex // 👈 状態をロックするための鍵
	isExiting       bool       // 👈 撤収作業中かどうかのフラグ
	AccountType     market.AccountType
	Exchange        market.ExchangeMarket
	MarginTradeType market.MarginTradeType
}

// NewSniper の引数と戻り値も修正
func NewSniper(symbol string, strategy Strategy) *Sniper {
	return &Sniper{
		Symbol:          symbol,
		Strategy:        strategy,
		Orders:          make([]*market.Order, 0),
		positions:       []market.Position{}, // 初期状態は空
		AccountType:     market.ACCOUNT_SPECIAL,
		Exchange:        market.EXCHANGE_TOSHO,
		MarginTradeType: market.TRADE_TYPE_GENERAL_DAY,
	}
}

// 価格の更新がされたと時に実行される監視ロジック
func (s *Sniper) Tick(state market.MarketState) *market.OrderRequest {
	// 処理中は他のゴルーチンが状態を触れないようにロック！
	s.mu.Lock()
	defer s.mu.Unlock() // 関数が終わったら必ずロック解除

	// すでにキルスイッチが作動（撤収中）なら、価格更新はすべて無視！
	if s.isExiting {
		return nil
	}

	// 1. 現在の建玉から必要なパラメータを計算（抽出）する
	var holdQty float64
	var totalExposure float64
	for _, p := range s.positions {
		holdQty += p.LeavesQty
		totalExposure += p.Price * float64(p.LeavesQty) // 取得単価 × 数量
	}

	// 発注済みで、まだ約定していない売り注文の「未約定数量」を合計する
	var pendingSellQty float64
	for _, order := range s.Orders { // スナイパーが管理している現在の注文リスト
		if order.Action == market.ACTION_SELL {
			// 注文した総数から、すでに約定した数を引く = まだ板に残っている数
			unexecutedQty := order.OrderQty - order.FilledQty()
			if unexecutedQty > 0 {
				pendingSellQty += unexecutedQty
			}
		}
	}

	// 戦略に渡す「自由に動かせる株数」
	freeQty := holdQty - pendingSellQty
	if freeQty < 0 {
		freeQty = 0 // 念のためのマイナス防止
	}

	averagePrice := 0.0
	if freeQty > 0 {
		averagePrice = totalExposure / float64(freeQty)
	}

	input := strategy.StrategyInput{
		CurrentPrice:  state.CurrentPrice,
		HoldQty:       freeQty,
		AveragePrice:  averagePrice,
		TotalExposure: totalExposure,
		ShortMA:       state.ShortMA,
		LongMA:        state.LongMA,
		VWAP:          state.VWAP,
	}

	// 1. 頭脳に価格を渡して判断を仰ぐ
	signal := s.Strategy.Evaluate(input)

	if signal.Action == brain.ACTION_HOLD {
		return nil // 何もしない
	}

	marketAction, err := signal.Action.ToMarketAction()
	if err != nil {
		fmt.Println("トラップできていないエラーがあります")
		return nil
	}

	return &market.OrderRequest{
		Symbol:             s.Symbol,
		Exchange:           s.Exchange,
		SecurityType:       market.SECURITY_TYPE_STOCK,
		Action:             marketAction,
		MarginTradeType:    market.TRADE_TYPE_GENERAL_DAY,
		AccountType:        market.ACCOUNT_SPECIAL,
		OrderType:          market.ORDER_TYPE_LIMIT,
		ClosePositionOrder: market.CLOSE_POSITION_ASC_DAY_DEC_PL,
		Qty:                signal.Quantity,
		Price:              signal.Price,
	}
}

// RecordOrder は、ユースケースが発注を完了した後に呼ばれ、状態を記録します
func (s *Sniper) RecordOrder(order market.Order) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.Orders = append(s.Orders, &order)
}

// ForceExit はキルスイッチ作動時に呼ばれ、自身の未約定注文のキャンセルと成行決済を行います
func (s *Sniper) ForceExit() {
	s.mu.Lock()
	s.isExiting = true // 撤収フラグを立てる！
	s.mu.Unlock()      // フラグを立てたら、通信で詰まらないように一旦ロック解除

	fmt.Printf("🚨 [%s] 撤収フラグON。これ以降の価格更新は無視し、強制決済プロセスを開始します。\n", s.Symbol)
}

// reducePositions は、指定された数量分だけ古い建玉から順に削減します
func (s *Sniper) reducePositions(sellQty float64) {
	remainingToSell := sellQty
	var newPositions []market.Position

	for _, p := range s.positions {
		if remainingToSell <= 0 {
			// 売却分を消化しきったら、残りの建玉はそのまま保持リストへ
			newPositions = append(newPositions, p)
			continue
		}

		if p.LeavesQty <= remainingToSell {
			// この建玉ロットを全量売却するケース
			remainingToSell -= p.LeavesQty
			// 全量売却なので newPositions には追加しない（消滅）
		} else {
			// この建玉ロットの一部だけを売却するケース
			p.LeavesQty -= remainingToSell
			remainingToSell = 0
			newPositions = append(newPositions, p)
		}
	}

	// 更新された建玉リストで上書き
	s.positions = newPositions
}

// OnExecution は、証券会社から約定通知を受信した際に呼び出されます
func (s *Sniper) OnExecution(report market.ExecutionReport) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. 対象の注文状態を更新する
	var matchedOrder *market.Order
	var matchedOrderIndex = -1
	for i, order := range s.Orders {
		if order.ID == report.OrderID {
			matchedOrder = order
			matchedOrderIndex = i
			break
		}
	}

	if matchedOrder == nil {
		fmt.Printf("⚠️ [%s] 未知の注文ID(%s)の約定通知を受信しました\n", s.Symbol, report.OrderID)
		return
	}

	// 注文エンティティに約定を追加
	matchedOrder.AddExecution(market.Execution{
		ID:    report.ExecutionID,
		Price: report.Price,
		Qty:   report.Qty,
	})

	// もし全約定していたら、Activeリストから消す（履歴用リストに移す等）
	if matchedOrder.IsCompleted() {
		if matchedOrderIndex != -1 {
			s.Orders = append(s.Orders[:matchedOrderIndex], s.Orders[matchedOrderIndex+1:]...)
		}
	}

	// 2. 実際の約定結果に基づいて、建玉（Positions）を更新する
	switch report.Action {
	case market.ACTION_BUY:
		s.positions = append(s.positions, market.Position{
			Symbol:    report.Symbol,
			LeavesQty: report.Qty,
			Price:     report.Price,
		})
		fmt.Printf("✅ [%s] 買付約定を反映: 単価%.2f 数量%f\n", s.Symbol, report.Price, report.Qty)
	case market.ACTION_SELL:
		s.reducePositions(report.Qty)
		fmt.Printf("✅ [%s] 売付約定を反映: 数量%f\n", s.Symbol, report.Qty)
	}
}
