// mock_server/main.go
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

type PushMessage struct {
	Symbol       string  `json:"Symbol"`
	SymbolName   string  `json:"SymbolName"`
	CurrentPrice float64 `json:"CurrentPrice"`
	Time         string  `json:"Time"`
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// 1. WebSocket配信用ハンドラー（以前と同じ）
func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("アップグレードエラー:", err)
		return
	}
	defer conn.Close()

	fmt.Println("[Mock] 🎯 ボットからのWebSocket接続を受け付けました！")
	basePrice := 4000.0

	for {
		msg := PushMessage{
			Symbol:       "9433",
			SymbolName:   "ＫＤＤＩ",
			CurrentPrice: basePrice,
			Time:         time.Now().Format("15:04:05"),
		}
		jsonData, _ := json.Marshal(msg)
		if err := conn.WriteMessage(websocket.TextMessage, jsonData); err != nil {
			break
		}
		basePrice += 2.0
		time.Sleep(2 * time.Second)
	}
}

// 2. トークン発行用のダミーハンドラー
func handleToken(w http.ResponseWriter, r *http.Request) {
	fmt.Println("[Mock] 🔑 トークン発行リクエストを受信しました")

	// API仕様通りのJSONを返す
	response := map[string]interface{}{
		"ResultCode": 0,
		"Token":      "mock_token_99999",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// 3. 建玉一覧取得用のダミーハンドラー
func handlePositions(w http.ResponseWriter, r *http.Request) {
	fmt.Println("[Mock] 📦 建玉照会リクエストを受信しました")

	// ダミーの建玉データ（KDDIを4000円で100株保有している状態）
	positions := []map[string]interface{}{
		{
			"ExecutionID":    "exec_mock_001",
			"Symbol":         "9433",
			"SymbolName":     "ＫＤＤＩ",
			"LeavesQty":      100.0,
			"HoldQty":        0.0,
			"Price":          4000.0, // ここが0.2%計算の基準になる建値
			"CurrentPrice":   4000.0,
			"Valuation":      400000.0,
			"ProfitLoss":     0.0,
			"ProfitLossRate": 0.0,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(positions)
}

// cmd/mock/main.go の handleSendOrder 関数を修正
func handleSendOrder(w http.ResponseWriter, r *http.Request) {
	fmt.Println("[Mock] 🔫 注文(SendOrder)リクエストを受信しました！")

	// タイムスタンプ（ナノ秒）を使ってユニークな受付IDを生成
	uniqueID := fmt.Sprintf("mock_order_%d", time.Now().UnixNano())

	// 成功レスポンス（Result: 0）と、生成したユニークIDを返す
	response := map[string]interface{}{
		"Result":  0,
		"OrderId": uniqueID,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)

	fmt.Printf("[Mock] 割り当てた受付ID: %s\n", uniqueID)
}

// mock_server/main.go に追記

// 5. 注文照会(Orders)用のダミーハンドラー
func handleOrders(w http.ResponseWriter, r *http.Request) {
	fmt.Println("[Mock] 📋 注文照会(Orders)リクエストを受信しました")

	// ダミーの注文データ（状態3：未約定で待機中の注文が1件ある想定）
	orders := []map[string]interface{}{
		{
			"ID":     "mock_active_order_001",
			"State":  3,
			"Symbol": "9433",
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(orders)
}

// 6. キャンセル(CancelOrder)用のダミーハンドラー
func handleCancelOrder(w http.ResponseWriter, r *http.Request) {
	fmt.Println("[Mock] 🛑 注文取消(Cancel)リクエストを受信しました！")

	response := map[string]interface{}{
		"Result":  0,
		"OrderId": "mock_active_order_001",
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func main() {
	// エンドポイントのルーティング
	http.HandleFunc("/kabusapi/websocket", handleWebSocket)
	http.HandleFunc("/kabusapi/token", handleToken)
	http.HandleFunc("/kabusapi/positions", handlePositions)
	http.HandleFunc("/kabusapi/sendorder", handleSendOrder)
	http.HandleFunc("/kabusapi/orders", handleOrders)
	http.HandleFunc("/kabusapi/cancelorder", handleCancelOrder)

	fmt.Println("[Mock] サーバー起動: モックkabuステーションがポート18080で待機中...")
	if err := http.ListenAndServe(":18080", nil); err != nil {
		log.Fatal("サーバー起動エラー:", err)
	}
}
