package engine

import (
	"fmt"
	"sync"
	"time"
	"trading-bot/internal/kabu"
)

// OrderState は発注した注文の追跡用データです
type OrderState struct {
	OrderID  string
	Action   TradeAction
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

// internal/engine/sniper.go の OnPriceUpdate 関数を修正

func (s *Sniper) OnPriceUpdate(currentPrice float64) {
	// 処理中は他のゴルーチンが状態を触れないようにロック！
	s.mu.Lock()
	defer s.mu.Unlock() // 関数が終わったら必ずロック解除

	// すでにキルスイッチが作動（撤収中）なら、価格更新はすべて無視！
	if s.isExiting {
		return
	}

	// 1. 戦略に「今どうすべきか？」の判断を仰ぐ
	signal := s.Strategy.Evaluate(currentPrice)

	// 2. 何もしない（HOLD）なら即終了
	if signal.Action == ActionHold {
		return
	}

	// 3. 執行（発注APIを実際に叩く）
	fmt.Printf("🔥【執行】命令を受理。%s: %s を %d株 発注します！\n",
		signal.Action, s.Symbol, signal.Quantity)

	// ※ご自身の data.go の定義に合わせてリクエストを作成してください
	// ここは成行売りのリクエスト例です
	orderReq := kabu.OrderRequest{ // ← data.goの定義名に合わせてください
		Password: "your_test_password",
		Symbol:   s.Symbol,
		// Exchange, SecurityType, Side(売), Qty(数量), FrontOrderType(成行) など必要な項目をセット
	}

	// 実際にモックサーバー（または本番）へ発注リクエストを送信！
	resp, err := s.Client.SendOrder(orderReq)
	if err != nil {
		fmt.Printf("❌ 発注エラー (%s): %v\n", s.Symbol, err)
		return // 失敗した場合はスライスに記録せず、次のチャンスを待つ
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
