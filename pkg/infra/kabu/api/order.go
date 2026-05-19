package api

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// OrderState は注文の状態を表します
const (
	STATE_WAITING    int32 = 1 // 待機（発注待機）
	STATE_PROCESSING int32 = 2 // 処理中（発注送信中）
	STATE_PROCESSED  int32 = 3 // 処理済（発注済・訂正済）
	STATE_CANCELING  int32 = 4 // 訂正取消送信中
	STATE_FINISHED   int32 = 5 // 終了（発注エラー・取消済・全約定・失効・期限切れ）
)

// RecType は明細の種別を表します
const (
	RECTYPE_RECEIVE    int32 = 1 // 受付
	RECTYPE_CARRY_OVER int32 = 2 // 繰越
	RECTYPE_EXPIRED    int32 = 3 // 期限切れ
	RECTYPE_ORDERED    int32 = 4 // 発注
	RECTYPE_EDITED     int32 = 5 // 訂正
	RECTYPE_CANCELED   int32 = 6 // 取消
	RECTYPE_INVALID    int32 = 7 // 失効
	RECTYPE_EXECUTION  int32 = 8 // 約定
)

type ClosePosition struct {
	HoldID string  `json:"HoldID"`
	Qty    float64 `json:"Qty"`
}

// OrderRequest は新規・決済注文を発注するためのリクエストデータです
// https://kabucom.github.io/kabusapi/reference/index.html#operation/sendorderPost
type OrderRequest struct {
	Symbol             string          `json:"Symbol"`                       // 銘柄コード (例: "9434")
	Exchange           ExchageType     `json:"Exchange"`                     // 市場コード (1: 東証)
	SecurityType       int             `json:"SecurityType"`                 // 商品種別 (1: 株式)
	Side               string          `json:"Side"`                         // 売買区分 ("1": 売, "2": 買)
	CashMargin         int             `json:"CashMargin"`                   // 信用区分 (1: 現物, 2: 信用新規, 3: 信用返済)
	MarginTradeType    int             `json:"MarginTradeType"`              // 信用取引区分 (1: 制度信用, 3: 一般信用デイトレ)
	AccountType        int             `json:"AccountType"`                  // 口座種別 (4: 特定口座)
	Qty                float64         `json:"Qty"`                          // 注文数量
	Price              float64         `json:"Price"`                        // 注文価格 (0: 成行)
	ExpireDay          int             `json:"ExpireDay"`                    // 注文有効期限 (0: 当日)
	FrontOrderType     int32           `json:"FrontOrderType"`               // 執行条件 (10: 成行, 20: 指値)
	DelivType          int32           `json:"DelivType"`                    // 受渡区分 (0: 指定なし, 2: お預かり金, 3: Auマネーコネクト)
	ClosePositionOrder *int32          `json:"ClosePositionOrder,omitempty"` // 決済順序
	ClosePositions     []ClosePosition `json:"ClosePositions,omitempty"`     // 指定返済
}

// OrderResponse は発注後のレスポンスデータです
type OrderResponse struct {
	Result  int    `json:"Result"`  // 結果コード (0: 成功)
	OrderId string `json:"OrderId"` // 受付番号 (注文の追跡やキャンセルに使う超重要ID)
}

type Order struct {
	ID         string        `json:"ID"`         // 注文ID（キャンセル時に必要）
	State      int32         `json:"State"`      // 状態（3: 処理中/待機中, 5: 終了 など）
	OrderState int32         `json:"OrderState"` // 注文状態
	Symbol     string        `json:"Symbol"`     // 銘柄コード
	Side       Side          `json:"Side"`       // 売買区分
	OrderQty   float64       `json:"OrderQty"`   // 発注数量
	CumQty     float64       `json:"CumQty"`     // 約定数量
	Price      float64       `json:"Price"`      // 値段
	Details    []OrderDetail `json:"Details"`    // 注文詳細（約定履歴やキャンセル等）
}

type OrderDetail struct {
	ID            string  `json:"ExecutionID"`   // 約定IDまたは注文詳細ID
	Price         float64 `json:"Price"`         // 約定値段
	Qty           float64 `json:"Qty"`           // 約定数量
	ExecutionDay  string  `json:"ExecutionDay"`  // 🌟 約定日時 (カブコムAPIキー)
	RecType       int32   `json:"RecType"`       // 明細種別（8: 約定, 6: 取消, 3: 期限切れ, 7: 失効）
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

	var orderResp OrderResponse
	if err := c.DecodeResponse(resp, &orderResp); err != nil {
		return nil, fmt.Errorf("発注失敗: %w", err)
	}

	// サーバーからエラーが返ってきていないかチェック
	if orderResp.Result != 0 {
		return nil, fmt.Errorf("発注失敗 (ResultCode: %d)", orderResp.Result)
	}

	if orderResp.OrderId == "" {
		return nil, fmt.Errorf("発注は成功しましたが、受付番号(OrderId)が空です")
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

	var cancelResp CancelResponse
	if err := c.DecodeResponse(resp, &cancelResp); err != nil {
		return nil, fmt.Errorf("キャンセル失敗: %w", err)
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

	var orders []Order
	if err := c.DecodeResponse(resp, &orders); err != nil {
		return nil, fmt.Errorf("注文取得失敗: %w", err)
	}

	return orders, nil
}
