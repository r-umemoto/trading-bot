package api

import (
	"encoding/json"
	"fmt"
)

// Position は1つの建玉（現在保有しているポジション）を表します
type Position struct {
	ExecutionID     string      `json:"ExecutionID"`     // 約定番号（決済指定時に使う）
	Exchange        ExchageType `json:"Exchange"`        //
	AccountType     int32       `json:"AccountType"`     //
	MarginTradeType int32       `json:"MarginTradeType"` //
	Side            string      `json:"Side"`            //
	Symbol          string      `json:"Symbol"`          // 銘柄コード (例: "7012")
	SymbolName      string      `json:"SymbolName"`      // 銘柄名
	LeavesQty       float64     `json:"LeavesQty"`       // 残数量（いま決済できる株数）
	HoldQty         float64     `json:"HoldQty"`         // 拘束数量（すでに売り注文を出して待機中の株数）
	Price           float64     `json:"Price"`           // 建値（平均取得単価） ★0.2%計算の基準！
	CurrentPrice    float64     `json:"CurrentPrice"`    // 現在値
	Valuation       float64     `json:"Valuation"`       // 評価金額
	ProfitLoss      float64     `json:"ProfitLoss"`      // 評価損益
	ProfitLossRate  float64     `json:"ProfitLossRate"`  // 評価損益率
}

// GetPositions は現在の建玉一覧を取得します。
func (c *KabuClient) GetPositions(product ProductType) ([]Position, error) {
	// エンドポイントにクエリパラメータを付与
	endpoint := fmt.Sprintf("/positions?product=%s", product)

	// GETリクエストを送信
	resp, err := c.doRequest("GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("建玉照会API通信エラー: %v", err)
	}
	defer resp.Body.Close()

	// レスポンスは JSON の配列（[]Position）として返ってくる
	var positions []Position
	if err := json.NewDecoder(resp.Body).Decode(&positions); err != nil {
		return nil, fmt.Errorf("建玉データ解析エラー: %v", err)
	}

	return positions, nil
}
