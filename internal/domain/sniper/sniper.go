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

// OrderState は発注した注文の追跡用データです
type OrderState struct {
	OrderID  string
	Action   market.Action
	Quantity float64
	IsClosed bool
}

// スナイパーが要求する「注文執行機能」の規格
type OrderExecutor interface {
	ExecuteOrder(symbol string, action brain.Action, qty int) (OrderState, error)
	CancelOrder(orderID string) error
	GetPositions(product market.ProductType) ([]market.Position, error)
}

// ★ スナイパー内で定義する「オプショナルな機能」の規格
type KillSwitchable interface {
	Activate() brain.Signal
}

// Sniper は戦略とAPIクライアントを持ち、執行を担います
type Sniper struct {
	Symbol    string
	positions []market.Position
	Strategy  Strategy
	Orders    []*OrderState
	mu        sync.Mutex // 👈 状態をロックするための鍵
	isExiting bool       // 👈 撤収作業中かどうかのフラグ
}

// NewSniper の引数と戻り値も修正
func NewSniper(symbol string, strategy Strategy) *Sniper {
	return &Sniper{
		Symbol:    symbol,
		Strategy:  strategy,
		Orders:    make([]*OrderState, 0),
		positions: []market.Position{}, // 初期状態は空
	}
}

// 価格の更新がされたと時に実行される監視ロジック
func (s *Sniper) Tick(currentPrice float64) *market.OrderRequest {
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
		holdQty += p.Qty
		totalExposure += p.Price * float64(p.Qty) // 取得単価 × 数量
	}

	averagePrice := 0.0
	if holdQty > 0 {
		averagePrice = totalExposure / float64(holdQty)
	}

	input := strategy.StrategyInput{
		CurrentPrice:  currentPrice,
		HoldQty:       holdQty,
		AveragePrice:  averagePrice,
		TotalExposure: totalExposure,
	}

	// 1. 頭脳に価格を渡して判断を仰ぐ
	signal := s.Strategy.Evaluate(input)

	if signal.Action == brain.ActionHold {
		return nil // 何もしない
	}

	marketAction, err := signal.ToMarketAction()
	if err != nil {
		fmt.Println("トラップできていないエラーがあります")
		return nil
	}

	return &market.OrderRequest{
		Symbol: s.Symbol,
		Action: marketAction,
		Qty:    signal.Quantity,
	}
}

// RecordOrder は、ユースケースが発注を完了した後に呼ばれ、状態を記録します
func (s *Sniper) RecordOrder(orderID string, action market.Action, qty float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.Orders = append(s.Orders, &OrderState{
		OrderID:  orderID,
		Action:   action,
		Quantity: qty,
		IsClosed: false,
	})
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

		if p.Qty <= remainingToSell {
			// この建玉ロットを全量売却するケース
			remainingToSell -= p.Qty
			// 全量売却なので newPositions には追加しない（消滅）
		} else {
			// この建玉ロットの一部だけを売却するケース
			p.Qty -= remainingToSell
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
	var matchedOrder *OrderState
	for _, order := range s.Orders {
		if order.OrderID == report.OrderID {
			matchedOrder = order
			order.IsClosed = true
			break
		}
	}

	if matchedOrder == nil {
		fmt.Printf("⚠️ [%s] 未知の注文ID(%s)の約定通知を受信しました\n", s.Symbol, report.OrderID)
	}

	// 2. 実際の約定結果に基づいて、建玉（Positions）を更新する
	switch report.Action {
	case market.Buy:
		s.positions = append(s.positions, market.Position{
			Symbol: report.Symbol,
			Qty:    report.Qty,
			Price:  report.Price,
		})
		fmt.Printf("✅ [%s] 買付約定を反映: 単価%.2f 数量%d\n", s.Symbol, report.Price, report.Qty)
	case market.Sell:
		s.reducePositions(report.Qty)
		fmt.Printf("✅ [%s] 売付約定を反映: 数量%d\n", s.Symbol, report.Qty)
	}
}
