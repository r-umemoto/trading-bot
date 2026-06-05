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

// KabuAPIError はkabuステーションAPIが正常コード(200)以外を返した際のエラー情報です
type KabuAPIError struct {
	StatusCode int
	Body       string
	Code       int    // APIから返されるエラーコード (例: 8)
	Message    string // APIから返されるエラーメッセージ
}

func (e *KabuAPIError) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("APIエラー (Status: %d, Code: %d): %s", e.StatusCode, e.Code, e.Message)
	}
	return fmt.Sprintf("APIエラー (Status: %d): %s", e.StatusCode, e.Body)
}

// IsClientError は HTTP 400 系のエラー、またはカブコムのエラーコード 8 (決済指定内容誤り) など、
// クライアント起因のエラーで即時失敗とすべきエラーであるかを判定します。
func (e *KabuAPIError) IsClientError() bool {
	// Status が 400, 401, 403, 422, 429 の場合はクライアントエラー
	if e.StatusCode == http.StatusBadRequest ||
		e.StatusCode == http.StatusUnauthorized ||
		e.StatusCode == http.StatusForbidden ||
		e.StatusCode == http.StatusUnprocessableEntity ||
		e.StatusCode == http.StatusTooManyRequests {
		return true
	}

	// エラーコード 8 (決済指定内容誤り) など、特定のカブコムエラーコードの場合
	if e.Code == 8 {
		return true
	}

	return false
}

// DecodeResponse はHTTPレスポンスのステータスコードをチェックし、正常であればJSONデコードを行います
func (c *KabuClient) DecodeResponse(resp *http.Response, out interface{}) error {
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)

		var errResp struct {
			Code    int    `json:"Code"`
			Message string `json:"Message"`
		}
		_ = json.Unmarshal(body, &errResp) // パース失敗時はデフォルト値 (Code: 0, Message: "")

		return &KabuAPIError{
			StatusCode: resp.StatusCode,
			Body:       string(body),
			Code:       errResp.Code,
			Message:    errResp.Message,
		}
	}

	if out == nil {
		return nil
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("レスポンス解析エラー: %v", err)
	}

	return nil
}
