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

// 1. 固定で返していた建玉データを「書き換え可能な変数」として外に出す
var mockPositions = []map[string]interface{}{
	{
		"ExecutionID": "exec_001",
		"AccountType": 4,
		"Symbol":      "9433",
		"SymbolName":  "ＫＤＤＩ",
		"SettleType":  0,
		"LeavesQty":   100.0, // 👈 最初は100株持っている
		"HoldQty":     100.0,
		"Price":       4000.0,
	},
}

// 3. 建玉一覧取得用のダミーハンドラー
func handlePositions(w http.ResponseWriter, r *http.Request) {
	fmt.Println("[Mock] 📦 建玉照会リクエストを受信しました")

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(mockPositions)
}

// cmd/mock/main.go の handleSendOrder 関数を修正
func handleSendOrder(w http.ResponseWriter, r *http.Request) {
	fmt.Println("\n[Mock] 🔫 注文(SendOrder)リクエストを受信しました！")

	// 1. ボットから送られてきた注文データ（JSON）を読み解く
	var req struct {
		Symbol string  `json:"Symbol"`
		Side   string  `json:"Side"` // "1": 売, "2": 買
		Qty    float64 `json:"Qty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
		actionStr := "不明"
		switch req.Side {
		case "1":
			actionStr = "売"
		case "2":
			actionStr = "買"
		}
		fmt.Printf("[Mock] 注文内容: 【%s】 銘柄: %s, 数量: %.0f株\n", actionStr, req.Symbol, req.Qty)

		// 2. 買い注文の場合（建玉を増やす）
		switch req.Side {
		case "2":
			// 今回はシンプルに新しい建玉データとして追加します
			mockPositions = append(mockPositions, map[string]interface{}{
				"ExecutionID": fmt.Sprintf("exec_%d", time.Now().UnixNano()),
				"Symbol":      req.Symbol,
				"SymbolName":  "シミュレーション銘柄",
				"LeavesQty":   req.Qty,
				"Price":       4000.0, // 仮の約定価格
			})
			fmt.Printf("[Mock] 📈 %s の建玉が %.0f株 追加されました。\n", req.Symbol, req.Qty)

			// 3. 売り注文の場合（建玉を減らす）
		case "1":
			var newPositions []map[string]interface{}
			for _, pos := range mockPositions {
				if pos["Symbol"] == req.Symbol {
					// 今持っている株数から、売った株数を引き算する
					currentQty := pos["LeavesQty"].(float64)
					newQty := currentQty - req.Qty

					if newQty > 0 {
						pos["LeavesQty"] = newQty // 減らした状態にして残す
						newPositions = append(newPositions, pos)
						fmt.Printf("[Mock] 📉 %s の建玉が %.0f株 に減りました（一部決済）。\n", req.Symbol, newQty)
					} else {
						// 0株以下になったら、配列から完全に消し去る
						fmt.Printf("[Mock] 🗑️ %s の建玉がゼロになったため削除しました（完全決済）。\n", req.Symbol)
					}
				} else {
					// 違う銘柄の建玉はそのまま残す
					newPositions = append(newPositions, pos)
				}
			}
			// 更新された状態を上書き保存
			mockPositions = newPositions
		}
	} else {
		fmt.Printf("[Mock] ⚠️ リクエストの解析に失敗しました: %v\n", err)
	}

	// 4. いつも通りユニークな受付IDを返す
	uniqueID := fmt.Sprintf("mock_order_%d", time.Now().UnixNano())
	response := map[string]interface{}{
		"Result":  0,
		"OrderId": uniqueID,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
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
