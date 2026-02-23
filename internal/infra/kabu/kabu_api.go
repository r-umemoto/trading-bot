package kabu

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
	"trading-bot/internal/domain/market"
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

// --- 以下、ビジネスロジック（各APIの実装） ---

// GetToken はパスワードを使って認証を行い、クライアント自身にトークンをセットします
func (c *KabuClient) GetToken() error {
	reqBody := TokenRequest{APIPassword: c.ApiPassword}
	jsonData, _ := json.Marshal(reqBody)

	// 内部メソッドを使うのでエンドポイント以下の指定だけで済む
	resp, err := c.doRequest("POST", "/token", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("API通信エラー: %v", err)
	}
	defer resp.Body.Close()

	var tokenResp TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return fmt.Errorf("レスポンス解析エラー: %v", err)
	}

	if tokenResp.ResultCode != 0 {
		return fmt.Errorf("トークン取得失敗 (ResultCode: %d)", tokenResp.ResultCode)
	}

	// 取得したトークンをクライアント自身に保持させる
	c.Token = tokenResp.Token
	return nil
}

// GetBoard は指定した銘柄の板情報を取得します
func (c *KabuClient) GetBoard(symbol string) (*BoardResponse, error) {
	endpoint := fmt.Sprintf("/board/%s@1", symbol) // @1は東証

	// ヘッダーセットなどのボイラープレートは一切不要になる
	resp, err := c.doRequest("GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("API通信エラー: %v", err)
	}
	defer resp.Body.Close()

	var boardResp BoardResponse
	if err := json.NewDecoder(resp.Body).Decode(&boardResp); err != nil {
		return nil, fmt.Errorf("レスポンス解析エラー: %v", err)
	}

	return &boardResp, nil
}

// kabu_api.go の末尾に追加

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

// GetPositions は現在の建玉一覧を取得します。
func (c *KabuClient) GetPositions(product ProductType) ([]Position, error) {
	// エンドポイントにクエリパラメータを付与
	endpoint := fmt.Sprintf("/positions?product=%s", product)

	// GETリクエストを送信
	resp, err := c.doRequest("GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("建玉照会API通信エラー: %v", err)
	}
	defer resp.Body.Close()

	// レスポンスは JSON の配列（[]Position）として返ってくる
	var positions []Position
	if err := json.NewDecoder(resp.Body).Decode(&positions); err != nil {
		return nil, fmt.Errorf("建玉データ解析エラー: %v", err)
	}

	return positions, nil
}

// kabu_api.go の末尾に追加

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
func (c *KabuClient) GetOrders() ([]market.Order, error) {
	resp, err := c.doRequest("GET", "/orders", nil)
	if err != nil {
		return nil, fmt.Errorf("注文照会API通信エラー: %v", err)
	}
	defer resp.Body.Close()

	var orders []market.Order
	if err := json.NewDecoder(resp.Body).Decode(&orders); err != nil {
		return nil, fmt.Errorf("注文データ解析エラー: %v", err)
	}

	return orders, nil
}
