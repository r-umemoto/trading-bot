package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// KabuClient はkabuステーションAPIと通信するためのクライアント構造体です
type KabuClient struct {
	BaseURL     string
	Token       string
	ApiPassword string
	HTTPClient  *http.Client
}

// NewKabuClient は新しいAPIクライアントを生成するコンストラクタです
func NewKabuClient(config Config) *KabuClient {
	return &KabuClient{
		BaseURL: config.APIURL,
		HTTPClient: &http.Client{
			Timeout: 10 * time.Second, // タイムアウトをデフォルトでDI
		},
		ApiPassword: config.Password,
	}
}

// doRequest はすべてのAPI呼び出しの基盤となる内部メソッドです。
// ここでURLの結合と、共通ヘッダー（トークンなど）のセットを必ず行います。
func (c *KabuClient) doRequest(method, endpoint string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, c.BaseURL+endpoint, body)
	if err != nil {
		return nil, err
	}

	// 共通ヘッダーの自動セット
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("X-API-KEY", c.Token)
	}

	return c.HTTPClient.Do(req)
}

// DecodeResponse はHTTPレスポンスのステータスコードをチェックし、正常であればJSONデコードを行います
func (c *KabuClient) DecodeResponse(resp *http.Response, out interface{}) error {
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("APIエラー (Status: %d): %s", resp.StatusCode, string(body))
	}

	if out == nil {
		return nil
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("レスポンス解析エラー: %v", err)
	}

	return nil
}
