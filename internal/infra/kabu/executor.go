// internal/infra/kabu/executor.go (新規作成)
package kabu

import (
	"fmt"
	"trading-bot/internal/domain/sniper"
	"trading-bot/internal/domain/sniper/brain"
)

// KabuExecutor は sniper.OrderExecutor インターフェースを満たすアダプター
type KabuExecutor struct {
	client      *KabuClient
	apiPassword string
}

func NewKabuExecutor(client *KabuClient, apiPassword string) *KabuExecutor {
	return &KabuExecutor{
		client:      client,
		apiPassword: apiPassword,
	}
}

// ExecuteOrder で、ドメインの言葉をカブコムのAPIリクエスト（kabu.OrderRequest）に翻訳して送信する
func (e *KabuExecutor) ExecuteOrder(symbol string, action brain.Action, qty int) (sniper.OrderState, error) {
	side := "1" // 売
	if action == brain.ActionBuy {
		side = "2" // 買
	}

	req := OrderRequest{
		Password:       e.apiPassword,
		Symbol:         symbol,
		Exchange:       1,
		SecurityType:   1,
		Side:           side,
		Qty:            qty,
		FrontOrderType: 10, // 成行
		Price:          0,
	}

	resp, err := e.client.SendOrder(req)
	if err != nil {
		return sniper.OrderState{}, fmt.Errorf("カブコムAPI発注失敗: %w", err)
	}

	return sniper.OrderState{
		OrderID:  resp.OrderId,
		Action:   action,
		Quantity: qty,
		IsClosed: false,
	}, nil
}

func (e *KabuExecutor) CancelOrder(orderID string) error {
	req := CancelRequest{OrderID: orderID, Password: e.apiPassword}
	_, err := e.client.CancelOrder(req)
	if err != nil {
		return fmt.Errorf("キャンセル失敗 (ResultCode: %s)", orderID)
	}
	return nil
}

func (e *KabuExecutor) GetPositions(product string) ([]sniper.Position, error) {

	positions, err := e.client.GetPositions(product)
	if err != nil {
		return nil, fmt.Errorf("建玉取得失敗: %s)", product)
	}

	decodePositons := make([]sniper.Position, 0, len(positions))
	for _, pos := range positions {
		decodePositons = append(decodePositons, sniper.Position{
			Symbol: pos.Symbol,
			Qty:    pos.LeavesQty,
		})
	}

	return decodePositons, nil
}
