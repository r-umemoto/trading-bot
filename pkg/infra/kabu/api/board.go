package api

import (
	"encoding/json"
	"fmt"
)

// 板情報レスポンス用（APIから返ってくる価格データの一部）
// ※実際のレスポンスはもっと巨大ですが、スナイパーボットに必要なものだけ抜粋します
type BoardResponse struct {
	Symbol       string  `json:"Symbol"`       // 銘柄コード
	SymbolName   string  `json:"SymbolName"`   // 銘柄名
	CurrentPrice float64 `json:"CurrentPrice"` // 現在値

	// 最良売気配（一番安く売ってくれる人の価格と数量）
	Sell1 struct {
		Price float64 `json:"Price"`
		Qty   float64 `json:"Qty"`
	} `json:"Sell1"`

	// 最良買気配（一番高く買ってくれる人の価格と数量）
	Buy1 struct {
		Price float64 `json:"Price"`
		Qty   float64 `json:"Qty"`
	} `json:"Buy1"`
}

// GetBoard は指定した銘柄の板情報を取得します
func (c *KabuClient) GetBoard(symbol string) (*BoardResponse, error) {
	endpoint := fmt.Sprintf("/board/%s@1", symbol) // @1は東証

	// ヘッダーセットなどのボイラープレートは一切不要になる
	resp, err := c.doRequest("GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("API通信エラー: %v", err)
	}
	defer resp.Body.Close()

	var boardResp BoardResponse
	if err := json.NewDecoder(resp.Body).Decode(&boardResp); err != nil {
		return nil, fmt.Errorf("レスポンス解析エラー: %v", err)
	}

	return &boardResp, nil
}
