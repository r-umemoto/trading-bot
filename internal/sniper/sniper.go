package sniper

import (
	"fmt"
	"sync"
	"time"
	"trading-bot/internal/kabu"
	"trading-bot/internal/sniper/brain"
)

// すべての戦略が満たすべき頭脳の規格
type Strategy interface {
	Evaluate(currentPrice float64) brain.Signal
}

// ★ スナイパー内で定義する「オプショナルな機能」の規格
type KillSwitchable interface {
	Activate() brain.Signal
}

// OrderState は発注した注文の追跡用データです
type OrderState struct {
	OrderID  string
	Action   brain.Action
	Quantity int
	IsClosed bool
}

// Sniper は戦略とAPIクライアントを持ち、執行を担います
type Sniper struct {
	Symbol    string
	Strategy  Strategy
	Client    *kabu.KabuClient // 👈 kabu. をつける
	Orders    []*OrderState
	mu        sync.Mutex // 👈 状態をロックするための鍵
	isExiting bool       // 👈 撤収作業中かどうかのフラグ
}

// NewSniper の引数と戻り値も修正
func NewSniper(symbol string, strategy Strategy, client *kabu.KabuClient) *Sniper {
	return &Sniper{
		Symbol:   symbol,
		Strategy: strategy,
		Client:   client,
		Orders:   make([]*OrderState, 0),
	}
}

func (s *Sniper) OnPriceUpdate(currentPrice float64) {
	// 処理中は他のゴルーチンが状態を触れないようにロック！
	s.mu.Lock()
	defer s.mu.Unlock() // 関数が終わったら必ずロック解除

	// すでにキルスイッチが作動（撤収中）なら、価格更新はすべて無視！
	if s.isExiting {
		return
	}

	// 1. 頭脳に価格を渡して判断を仰ぐ
	signal := s.Strategy.Evaluate(currentPrice)

	// 2. 受け取ったシグナルで発砲する
	s.executeSignal(signal)
}

// 🎯 新設：純粋な発砲処理（ダミー価格のハックが不要になる）
func (s *Sniper) executeSignal(signal brain.Signal) {
	if signal.Action == brain.ActionHold {
		return
	}

	side := "1"
	actionName := "売"
	if signal.Action == brain.ActionBuy {
		side = "2"
		actionName = "買"
	}

	fmt.Printf("🔥 [%s] シグナル検知！ %s %d株を成行発注します\n", s.Symbol, actionName, signal.Quantity)

	// 3. 執行
	req := kabu.OrderRequest{
		Password:       "dummy_password", // 本番は安全な管理へ
		Symbol:         s.Symbol,
		Exchange:       1,
		SecurityType:   1,
		Side:           side,
		Qty:            signal.Quantity,
		FrontOrderType: 10, // 成行
		Price:          0,
	}

	resp, err := s.Client.SendOrder(req)
	if err != nil {
		fmt.Printf("❌ [%s] 発注エラー: %v\n", s.Symbol, err)
		return
	}

	// 4. モックサーバーから返ってきた「本物」のOrderIDを記録する
	s.Orders = append(s.Orders, &OrderState{
		OrderID:  resp.OrderId, // ← モックサーバーが発行した "mock_order_99999" 等が入る
		Action:   signal.Action,
		Quantity: signal.Quantity,
		IsClosed: false,
	})

	fmt.Printf("✅ 注文完了！状態を記録しました (API受付ID: %s)\n", resp.OrderId)
}

// ForceExit はキルスイッチ作動時に呼ばれ、自身の未約定注文のキャンセルと成行決済を行います
func (s *Sniper) ForceExit(apiPassword string) {
	s.mu.Lock()
	s.isExiting = true // 撤収フラグを立てる！
	s.mu.Unlock()      // フラグを立てたら、通信で詰まらないように一旦ロック解除

	fmt.Printf("🚨 [%s] 撤収フラグON。これ以降の価格更新は無視し、強制決済プロセスを開始します。\n", s.Symbol)

	// --- 第一段階：自分の持っている未約定注文をすべてキャンセル ---
	for _, order := range s.Orders {
		if !order.IsClosed {
			fmt.Printf("🛑 [%s] 注文(ID: %s)をキャンセル中...\n", s.Symbol, order.OrderID)
			req := kabu.CancelRequest{OrderID: order.OrderID, Password: apiPassword}
			_, err := s.Client.CancelOrder(req)
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
	positions, err := s.Client.GetPositions("2")
	if err != nil {
		fmt.Printf("❌ [%s] 建玉取得エラー: %v\n", s.Symbol, err)
		return
	}

	var remainingQty int
	for _, pos := range positions {
		if pos.Symbol == s.Symbol { // 自分の担当銘柄だけを合算
			remainingQty += int(pos.LeavesQty)
		}
	}

	if remainingQty > 0 {
		fmt.Printf("🔥 [%s] 残存建玉 %d株 を成行で強制決済します！\n", s.Symbol, remainingQty)
		req := kabu.OrderRequest{
			Password:       apiPassword,
			Symbol:         s.Symbol,
			Exchange:       1,
			SecurityType:   1,
			Side:           "1", // 売
			Qty:            remainingQty,
			FrontOrderType: 10, // 成行
			Price:          0,
		}
		_, err := s.Client.SendOrder(req)
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
