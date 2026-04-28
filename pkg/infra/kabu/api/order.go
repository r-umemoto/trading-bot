package api

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type ClosePosition struct {
	HoldID string  `json:"HoldID"`
	Qty    float64 `json:"Qty"`
}

// OrderRequest は新規・決済注文を発注するためのリクエストデータです
// https://kabucom.github.io/kabusapi/reference/index.html#operation/sendorderPost
type OrderRequest struct {
	Symbol             string      `json:"Symbol"`             // 銘柄コード (例: "9434")
	Exchange           ExchageType `json:"Exchange"`           // 市場コード (1: 東証)
	SecurityType       int         `json:"SecurityType"`       // 商品種別 (1: 株式)
	Side               string      `json:"Side"`               // 売買区分 ("1": 売, "2": 買)
	CashMargin         int         `json:"CashMargin"`         // 信用区分 (1: 現物, 2: 信用新規, 3: 信用返済)
	MarginTradeType    int         `json:"MarginTradeType"`    // 信用取引区分 (1: 制度信用, 3: 一般信用デイトレ)
	AccountType        int         `json:"AccountType"`        // 口座種別 (4: 特定口座)
	Qty                float64     `json:"Qty"`                // 注文数量
	Price              float64     `json:"Price"`              // 注文価格 (0: 成行)
	ExpireDay          int         `json:"ExpireDay"`          // 注文有効期限 (0: 当日)
	FrontOrderType     int32       `json:"FrontOrderType"`     // 執行条件 (10: 成行, 20: 指値)
	DelivType          int32           `json:"DelivType"`          // 受渡区分 (0: 指定なし, 2: お預かり金, 3: Auマネーコネクト)
	ClosePositionOrder *int32           `json:"ClosePositionOrder,omitempty"` // 決済順序
	ClosePositions     []ClosePosition `json:"ClosePositions,omitempty"` // 指定返済
}

// OrderResponse は発注後のレスポンスデータです
type OrderResponse struct {
	Result  int    `json:"Result"`  // 結果コード (0: 成功)
	OrderId string `json:"OrderId"` // 受付番号 (注文の追跡やキャンセルに使う超重要ID)
}

type Order struct {
	ID       string      `json:"ID"`       // 注文ID（キャンセル時に必要）
	State    int32       `json:"State"`    // 状態（3: 処理中/待機中, 5: 終了 など）
	Symbol   string      `json:"Symbol"`   // 銘柄コード
	Side     Side        `json:"Side"`     // 売買区分
	OrderQty float64     `json:"OrderQty"` // 発注数量
	CumQty   float64     `json:"CumQty"`   // 約定数量
	Price    float64     `json:"Price"`    // 値段
	Details  []Execution `json:"Details"`  // 値段
}

type Execution struct {
	ID    string  `json:"ExecutionID"` // 注文ID（キャンセル時に必要）
	Price float64 `json:"Price"`       // 約定値段
	Qty   float64 `json:"Qty"`         // 約定数量
}

// SendOrder は構成した注文リクエストをAPIに送信し、注文を実行します
func (c *KabuClient) SendOrder(req OrderRequest) (*OrderResponse, error) {
	// 構造体をJSONに変換
	jsonData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("注文データのJSON変換エラー: %v", err)
	}

	// POSTリクエストを送信 (エンドポイントは /sendorder)
	resp, err := c.doRequest("POST", "/sendorder", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("発注API通信エラー: %v", err)
	}
	defer resp.Body.Close()

	// レスポンスを受け止める
	var orderResp OrderResponse
	if err := json.NewDecoder(resp.Body).Decode(&orderResp); err != nil {
		return nil, fmt.Errorf("発注レスポンス解析エラー: %v", err)
	}

	// サーバーからエラーが返ってきていないかチェック
	if orderResp.Result != 0 {
		return nil, fmt.Errorf("発注失敗 (ResultCode: %d)", orderResp.Result)
	}

	return &orderResp, nil
}

// CancelRequest は注文取消用のリクエストデータです
type CancelRequest struct {
	OrderID string `json:"OrderId"` // 取消したい注文の受付番号
}

// CancelResponse は注文取消後のレスポンスデータです
type CancelResponse struct {
	Result  int    `json:"Result"`
	OrderID string `json:"OrderId"`
}

// CancelOrder は指定した注文IDの注文を取り消します
func (c *KabuClient) CancelOrder(req CancelRequest) (*CancelResponse, error) {
	jsonData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("キャンセルデータのJSON変換エラー: %v", err)
	}

	// キャンセルは PUT リクエスト
	resp, err := c.doRequest("PUT", "/cancelorder", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("キャンセルAPI通信エラー: %v", err)
	}
	defer resp.Body.Close()

	var cancelResp CancelResponse
	if err := json.NewDecoder(resp.Body).Decode(&cancelResp); err != nil {
		return nil, fmt.Errorf("キャンセルレスポンス解析エラー: %v", err)
	}

	if cancelResp.Result != 0 {
		return nil, fmt.Errorf("キャンセル失敗 (ResultCode: %d)", cancelResp.Result)
	}

	return &cancelResp, nil
}

// GetOrders は現在の注文一覧を取得します
func (c *KabuClient) GetOrders() ([]Order, error) {
	resp, err := c.doRequest("GET", "/orders", nil)
	if err != nil {
		return nil, fmt.Errorf("注文照会API通信エラー: %v", err)
	}
	defer resp.Body.Close()

	var orders []Order
	if err := json.NewDecoder(resp.Body).Decode(&orders); err != nil {
		return nil, fmt.Errorf("注文データ解析エラー: %v", err)
	}

	return orders, nil
}
