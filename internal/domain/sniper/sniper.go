package sniper

import (
	"fmt"
	"sync"
	"time"
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
	Action   brain.Action
	Quantity int
	IsClosed bool
}

type Position struct {
	Symbol string  // 銘柄
	Qty    uint32  // 数数
	Price  float64 // 取得価格
}

// スナイパーが要求する「注文執行機能」の規格
type OrderExecutor interface {
	ExecuteOrder(symbol string, action brain.Action, qty int) (OrderState, error)
	CancelOrder(orderID string) error
	GetPositions(product ProductType) ([]Position, error)
}

// ★ スナイパー内で定義する「オプショナルな機能」の規格
type KillSwitchable interface {
	Activate() brain.Signal
}

// Sniper は戦略とAPIクライアントを持ち、執行を担います
type Sniper struct {
	Symbol    string
	positions []Position
	Strategy  Strategy
	executor  OrderExecutor
	Orders    []*OrderState
	mu        sync.Mutex // 👈 状態をロックするための鍵
	isExiting bool       // 👈 撤収作業中かどうかのフラグ
}

// NewSniper の引数と戻り値も修正
func NewSniper(symbol string, strategy Strategy, excutor OrderExecutor) *Sniper {
	return &Sniper{
		Symbol:    symbol,
		Strategy:  strategy,
		executor:  excutor,
		Orders:    make([]*OrderState, 0),
		positions: []Position{}, // 初期状態は空
	}
}

// 価格の更新がされたと時に実行される監視ロジック
func (s *Sniper) Tick(currentPrice float64) {
	// 処理中は他のゴルーチンが状態を触れないようにロック！
	s.mu.Lock()
	defer s.mu.Unlock() // 関数が終わったら必ずロック解除

	// すでにキルスイッチが作動（撤収中）なら、価格更新はすべて無視！
	if s.isExiting {
		return
	}

	// 1. 現在の建玉から必要なパラメータを計算（抽出）する
	var holdQty uint32
	var totalExposure float64

	for _, p := range s.positions {
		holdQty += p.Qty
		totalExposure += p.Price * float64(p.Qty) // 取得単価 × 数量
	}

	averagePrice := 0.0
	if holdQty > 0 {
		averagePrice = totalExposure / float64(holdQty)
	}

	// 2. 計算済みのキレイなデータだけをInputに詰める
	input := strategy.StrategyInput{
		CurrentPrice:  currentPrice,
		HoldQty:       holdQty,
		AveragePrice:  averagePrice,
		TotalExposure: totalExposure,
	}

	// 1. 頭脳に価格を渡して判断を仰ぐ
	signal := s.Strategy.Evaluate(input)

	// 2. 受け取ったシグナルで発砲する
	s.executeSignal(signal)
}

// 🎯 新設：純粋な発砲処理
func (s *Sniper) executeSignal(signal brain.Signal) {
	if signal.Action == brain.ActionHold {
		return
	}

	fmt.Printf("🚀 [%s] 発注開始: %s %d株\n", s.Symbol, signal.Action, signal.Quantity)

	// APIへ注文を送信
	resp, err := s.executor.ExecuteOrder(s.Symbol, signal.Action, signal.Quantity)
	if err != nil {
		fmt.Printf("❌ [%s] 発注エラー: %v\n", s.Symbol, err)
		return
	}

	// 発注が受け付けられたら、未約定の注文としてリストに追加するだけ（建玉は増やさない）
	s.Orders = append(s.Orders, &resp)

	fmt.Printf("📝 [%s] 注文受付完了: ID=%s (約定待ち)\n", s.Symbol, resp.OrderID)
}

// ForceExit はキルスイッチ作動時に呼ばれ、自身の未約定注文のキャンセルと成行決済を行います
func (s *Sniper) ForceExit() {
	s.mu.Lock()
	s.isExiting = true // 撤収フラグを立てる！
	s.mu.Unlock()      // フラグを立てたら、通信で詰まらないように一旦ロック解除

	fmt.Printf("🚨 [%s] 撤収フラグON。これ以降の価格更新は無視し、強制決済プロセスを開始します。\n", s.Symbol)

	// --- 第一段階：自分の持っている未約定注文をすべてキャンセル ---
	for _, order := range s.Orders {
		if !order.IsClosed {
			fmt.Printf("🛑 [%s] 注文(ID: %s)をキャンセル中...\n", s.Symbol, order.OrderID)
			err := s.executor.CancelOrder(order.OrderID)
			if err != nil {
				fmt.Printf("❌ [%s] キャンセルエラー: %v\n", s.Symbol, err)
			} else {
				order.IsClosed = true // キャンセル完了として扱う
			}
		}
	}

	// --- 第二段階：証券会社側でのロック解除を待機 ---
	time.Sleep(2 * time.Second)

	// --- 第三段階：自分の担当銘柄の残ポジションを確認して成行売り ---
	positions, err := s.executor.GetPositions(ProductMargin)
	if err != nil {
		fmt.Printf("❌ [%s] 建玉取得エラー: %v\n", s.Symbol, err)
		return
	}

	var remainingQty int
	for _, pos := range positions {
		if pos.Symbol == s.Symbol { // 自分の担当銘柄だけを合算
			remainingQty += int(pos.Qty)
		}
	}

	if remainingQty > 0 {
		fmt.Printf("🔥 [%s] 残存建玉 %d株 を成行で強制決済します！\n", s.Symbol, remainingQty)
		_, err := s.executor.ExecuteOrder(s.Symbol, brain.ActionSell, remainingQty)
		if err != nil {
			fmt.Printf("❌ [%s] 成行決済エラー: %v\n", s.Symbol, err)
		} else {
			fmt.Printf("✅ [%s] 強制決済の発注を完了しました。\n", s.Symbol)
		}
	} else {
		fmt.Printf("✅ [%s] 残存建玉なし。撤収完了。\n", s.Symbol)
	}
}

// 緊急撤退命令を受信するメソッド
func (s *Sniper) EmergencyExit() {
	// ⚠️ ここではロックを取らない！（OnPriceUpdateの中で取ってくれるから）
	// ⚠️ s.isExiting = true もまだやらない！（弾かれてしまうから）

	// 1. キルスイッチを持っているか確認
	if ks, ok := s.Strategy.(KillSwitchable); ok {
		fmt.Printf("🚨 [%s] 緊急撤退命令を受理。戦略のキルスイッチを起動します！\n", s.Symbol)

		// 2. キルスイッチをON！
		emergencySignal := ks.Activate()

		s.executeSignal(emergencySignal)
	} else {
		fmt.Printf("⚠️ [%s] 現在の戦略にはキルスイッチが搭載されていません。\n", s.Symbol)
	}

	// 4. 最後に発砲が終わってから、スナイパーの稼働を完全に停止させる
	s.mu.Lock()
	s.isExiting = true
	s.mu.Unlock()
}

// reducePositions は、指定された数量分だけ古い建玉から順に削減します
func (s *Sniper) reducePositions(sellQty uint32) {
	remainingToSell := sellQty
	var newPositions []Position

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
		s.positions = append(s.positions, Position{
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
