// websocket.go
package api

import (
	"context"
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

// PushMessage はWebSocket経由で受信する時価・板情報メッセージです
// REST APIの BoardResponse と共通の構造を持ちます
type PushMessage BoardResponse

// Listen はサーバーに接続し、受信したデータをチャネル(ch)に流し続けます。
// この関数はGoroutineで非同期に実行されることを想定しています。
func (w *WSClient) Listen(ctx context.Context, ch chan<- PushMessage) error {
	// 1. サーバーへ接続
	fmt.Printf("WebSocket接続開始: %s\n", w.URL)
	conn, _, err := websocket.DefaultDialer.Dial(w.URL, nil)
	if err != nil {
		return fmt.Errorf("WebSocket接続エラー: %w", err)
	}
	defer conn.Close()
	fmt.Println("WebSocket接続成功！価格の監視をスタートします。")

	// contextキャンセル時に接続を閉じるためのGoroutine
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()

	// 2. データの受信ループ（切断されるまで無限ループ）
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				return fmt.Errorf("WebSocket読み取りエラー (切断されました): %w", err)
			}
		}

		// 3. 受け取ったJSONを構造体に変換
		var pushMsg PushMessage
		if err := json.Unmarshal(message, &pushMsg); err != nil {
			log.Printf("JSONパースエラー: %v", err)
			continue // エラーが起きても止まらずに次のデータを待つ
		}

		// 4. 解析したデータをチャネルを通じてメインロジックへ送る
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ch <- pushMsg:
		}
	}
}
