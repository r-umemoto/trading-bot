package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWSClient_Listen(t *testing.T) {
	// 1. テスト用の WebSocket サーバを起動
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// テスト用ダミーデータを送信
		dummyMessage := `{"Symbol":"7201","CurrentPrice":4000.0}`
		_ = conn.WriteMessage(websocket.TextMessage, []byte(dummyMessage))

		// クライアント側から切断されるのを待つ
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
		}
	}))
	defer server.Close()

	// http:// を ws:// に変換
	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	client := NewWSClient(wsURL)

	// 2. テストの実行管理用 Context
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch := make(chan PushMessage, 10)

	// 3. 非同期で Listen を開始
	errCh := make(chan error, 1)
	go func() {
		errCh <- client.Listen(ctx, ch)
	}()

	// 4. チャネルからのデータ受信をテスト
	select {
	case msg := <-ch:
		// 送信したダミーデータが正しく Unmarshal されているか検証
		if msg.Symbol != "7201" {
			t.Errorf("expected Symbol 7201, got %s", msg.Symbol)
		}
		if msg.CurrentPrice != 4000.0 {
			t.Errorf("expected CurrentPrice 4000.0, got %f", msg.CurrentPrice)
		}
		// 成功したら Context を即座にキャンセルして Listen を終了させる
		cancel()

	case <-time.After(100 * time.Millisecond):
		t.Fatal("テストタイムアウト: WebSocketメッセージを受信できませんでした")
	}

	// 5. Listen メソッドが正常に（Contextキャンセルで）終了したか確認
	select {
	case err := <-errCh:
		if err != nil && err != context.Canceled {
			t.Errorf("Listen returned unexpected error: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Listen が Context キャンセル後に即座に終了しませんでした")
	}
}
