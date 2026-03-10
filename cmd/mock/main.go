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

func main() {
	// エンドポイントのルーティング
	http.HandleFunc("/kabusapi/websocket", handleWebSocket)
	http.HandleFunc("/kabusapi/token", handleToken)
	http.HandleFunc("/kabusapi/positions", handlePositions)
	http.HandleFunc("/kabusapi/sendorder", handleSendOrder)
	http.HandleFunc("/kabusapi/orders", handleOrders)
	http.HandleFunc("/kabusapi/cancelorder", handleCancelOrder)
	http.HandleFunc("/kabusapi/register", handleRegister)
	http.HandleFunc("/kabusapi/unregister/all", handleUnregisterAll)

	fmt.Println("[Mock] サーバー起動: モックkabuステーションがポート18082で待機中...")
	if err := http.ListenAndServe(":18082", nil); err != nil {
		log.Fatal("サーバー起動エラー:", err)
	}
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

	// より現実的な変動をするモックの価格データ（数円〜数十円の範囲で動く）
	priceWave := []float64{
		4000.0, 3998.0, 3995.0,
		3990.0, // 🎯 [シナリオ1] LimitBuy(3990円以下) が発動
		3988.0, 3985.0, 3980.0, 3975.0,
		3970.0, 3965.0, 3975.0, // 底を打って上がり始める
		3985.0,
		3998.0, // 🎯 [シナリオ2] 利確など
		4000.0, 4005.0,
	}

	tick := 0

	// 動的な計算のための累積値（日産用）
	var nissanTotalVolume float64
	var nissanSumPriceVolume float64

	// 動的な計算のための累積値（SBG用）
	var sbgTotalVolume float64
	var sbgSumPriceVolume float64

	for {
		// 配列のインデックスをループさせる
		currentPrice := priceWave[tick%len(priceWave)]

		// ランダムな出来高（今回は簡単のために価格変動時に100〜500株の約定があったことにする擬似ロジック）
		// ここではモックなので固定の擬似乱数的な変動として、インデックスを利用しつつ多少ばらけさせます
		var volumeAdded float64 = float64(100 + (tick%5)*100)

		// 日産用の累積を更新
		nissanTotalVolume += volumeAdded
		nissanSumPriceVolume += currentPrice * volumeAdded
		var nissanVWAP float64 = currentPrice
		if nissanTotalVolume > 0 {
			nissanVWAP = nissanSumPriceVolume / nissanTotalVolume
		}

		// PushMessageの組み立て
		msg := map[string]interface{}{
			"Symbol":           "7201",
			"SymbolName":       "nissan",
			"CurrentPrice":     currentPrice,
			"VWAP":             nissanVWAP,
			"TradingVolume":    nissanTotalVolume,
			"CurrentPriceTime": time.Now().Format(time.RFC3339),
		}
		jsonData, _ := json.Marshal(msg)
		if err := conn.WriteMessage(websocket.TextMessage, jsonData); err != nil {
			break
		}
		fmt.Printf("🌊 モック相場変動: %+v \n", msg)

		// ソフトバンク用の累積を更新（少し違う動きにするためvolumeAddedを変える）
		volSBG := volumeAdded * 1.5
		sbgTotalVolume += volSBG
		sbgSumPriceVolume += currentPrice * volSBG
		var sbgVWAP float64 = currentPrice
		if sbgTotalVolume > 0 {
			sbgVWAP = sbgSumPriceVolume / sbgTotalVolume
		}

		msg2 := map[string]interface{}{
			"Symbol":           "9434",
			"SymbolName":       "softbank",
			"CurrentPrice":     currentPrice,
			"VWAP":             sbgVWAP,
			"TradingVolume":    sbgTotalVolume,
			"CurrentPriceTime": time.Now().Format(time.RFC3339),
		}
		jsonData2, _ := json.Marshal(msg2)
		if err := conn.WriteMessage(websocket.TextMessage, jsonData2); err != nil {
			break
		}
		fmt.Printf("🌊 モック相場変動: %+v \n", msg2)

		tick++
		time.Sleep(1 * time.Second) // 1秒ごとに価格を更新
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
		"ExecutionID":     "exec_001",
		"Exchange":        1,
		"AccountType":     4,
		"Symbol":          "7201",
		"SymbolName":      "sbg",
		"Side":            "2",
		"MarginTradeType": 3,
		"LeavesQty":       100.0, // 👈 最初は100株持っている
		"HoldQty":         100.0,
		"Price":           4000.0,
	},
}

var mockOrders = []map[string]interface{}{}

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
		Symbol         string  `json:"Symbol"`
		Side           string  `json:"Side"` // "1": 売, "2": 買
		Qty            float64 `json:"Qty"`
		Price          float64 `json:"Price"`
		FrontOrderType int     `json:"FrontOrderType"`
		AccountType    int32   `json:"AccountType"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
		actionStr := "不明"
		switch req.Side {
		case "1":
			actionStr = "売"
		case "2":
			actionStr = "買"
		}
		fmt.Printf("[Mock] 注文内容: 【%s】 銘柄: %s, 数量: %.0f株, 価格%.0f\n", actionStr, req.Symbol, req.Qty, req.Price)

		// 2. 買い注文の場合（建玉を増やす）
		switch req.Side {
		case "2":
			// 今回はシンプルに新しい建玉データとして追加します
			mockPositions = append(mockPositions, map[string]interface{}{
				"ExecutionID":     fmt.Sprintf("exec_%d", time.Now().UnixNano()),
				"Symbol":          req.Symbol,
				"SymbolName":      "シミュレーション銘柄",
				"LeavesQty":       req.Qty,
				"Price":           req.Price,
				"AccountType":     req.AccountType,
				"MarginTradeType": 3,
			})
			fmt.Printf("[Mock] 📈 %s の建玉が %.0f株 追加されました。\n", req.Symbol, req.Qty)

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

	uniqueExID := fmt.Sprintf("mock_order_ex_%d", time.Now().UnixNano())

	mockOrders = append(mockOrders, map[string]interface{}{
		"ID":          uniqueID,
		"Symbol":      req.Symbol,
		"State":       3,
		"Side":        req.Side,
		"CumQty":      req.Qty,
		"OrderQty":    req.Qty,
		"AccountType": req.AccountType,
		"Details": []map[string]interface{}{{
			"Price":       req.Price,
			"Qty":         req.Qty,
			"ExecutionID": uniqueExID,
		}},
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// mock_server/main.go に追記

// 5. 注文照会(Orders)用のダミーハンドラー
func handleOrders(w http.ResponseWriter, r *http.Request) {
	fmt.Println("[Mock] 📋 注文照会(Orders)リクエストを受信しました")

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(mockOrders)
}

// 6. キャンセル(CancelOrder)用のダミーハンドラー
func handleCancelOrder(w http.ResponseWriter, r *http.Request) {
	fmt.Println("[Mock] 🛑 注文取消(Cancel)リクエストを受信しました！")

	var req struct {
		OrderID string `json:"OrderId"` // 取消したい注文の受付番号
	}

	response := map[string]interface{}{
		"Result":  1,
		"OrderId": "",
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
		response = map[string]interface{}{
			"Result":  0,
			"OrderId": req.OrderID,
		}
	} else {
		fmt.Println("[Mock] ⚠️ 注文取消(Cancel)失敗")
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func handleRegister(w http.ResponseWriter, r *http.Request) {

	// API仕様通りのJSONを返す
	response := map[string]interface{}{
		"ResultCode": 0,
		"Token":      "mock_token_99999",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func handleUnregisterAll(w http.ResponseWriter, r *http.Request) {

	// API仕様通りのJSONを返す
	response := map[string]interface{}{
		"ResultCode": 0,
		"Token":      "mock_token_99999",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
