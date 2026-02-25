// websocket.go
package kabu

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/gorilla/websocket"
)

// WSClient はWebSocket通信を管理する構造体です
type WSClient struct {
	URL string
}

// NewWSClient はWebSocketクライアントを生成します
func NewWSClient(url string) *WSClient {
	return &WSClient{
		URL: url,
	}
}

// Listen はサーバーに接続し、受信したデータをチャネル(ch)に流し続けます。
// この関数はGoroutineで非同期に実行されることを想定しています。
func (w *WSClient) Listen(ch chan<- PushMessage) {
	// 1. サーバーへ接続
	fmt.Printf("WebSocket接続開始: %s\n", w.URL)
	conn, _, err := websocket.DefaultDialer.Dial(w.URL, nil)
	if err != nil {
		log.Fatalf("WebSocket接続エラー: %v", err)
	}
	defer conn.Close()
	fmt.Println("WebSocket接続成功！価格の監視をスタートします。")

	// 2. データの受信ループ（切断されるまで無限ループ）
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Printf("WebSocket読み取りエラー (切断されました): %v", err)
			return // ループを抜けてGoroutineを終了
		}

		// 3. 受け取ったJSONを構造体に変換
		var pushMsg PushMessage
		if err := json.Unmarshal(message, &pushMsg); err != nil {
			log.Printf("JSONパースエラー: %v", err)
			continue // エラーが起きても止まらずに次のデータを待つ
		}

		// 4. 解析したデータをチャネルを通じてメインロジック（脳）へ送る
		// ※ここがGoならではの最高に美しいデータ連携です
		ch <- pushMsg
	}
}
