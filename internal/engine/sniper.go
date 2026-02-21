package engine

import (
	"fmt"
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
	Symbol   string
	Strategy Strategy
	Client   *kabu.KabuClient // 👈 kabu. をつける
	Orders   []*OrderState
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
