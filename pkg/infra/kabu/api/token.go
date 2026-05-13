package api

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// トークン取得リクエスト用（こちらから送るデータ）
type TokenRequest struct {
	APIPassword string `json:"APIPassword"`
}

// トークン取得レスポンス用（APIから返ってくるデータ）
type TokenResponse struct {
	ResultCode int    `json:"ResultCode"`
	Token      string `json:"Token"`
}

// GetToken はパスワードを使って認証を行い、クライアント自身にトークンをセットします
func (c *KabuClient) GetToken() error {
	reqBody := TokenRequest{APIPassword: c.ApiPassword}
	jsonData, _ := json.Marshal(reqBody)

	resp, err := c.doRequest("POST", "/token", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("API通信エラー: %v", err)
	}

	var tokenResp TokenResponse
	if err := c.DecodeResponse(resp, &tokenResp); err != nil {
		return fmt.Errorf("トークン取得失敗: %w", err)
	}

	if tokenResp.ResultCode != 0 {
		return fmt.Errorf("トークン取得失敗 (ResultCode: %d)", tokenResp.ResultCode)
	}

	// 取得したトークンをクライアント自身に保持させる
	c.Token = tokenResp.Token
	return nil
}
