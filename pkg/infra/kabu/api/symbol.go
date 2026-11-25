package api

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type RegisterSymbolsItem struct {
	Symbol   string      `json:"Symbol"`
	Exchange ExchageType `json:"Exchange"`
}

type RegisterSymbolRequest struct {
	Symbols []RegisterSymbolsItem `json:"Symbols"`
}

type RegistListItem struct {
	Symbol   string      `json:"Symbol"`
	Exchange ExchageType `json:"Exchange"`
}

type RegisterSymbolResponse struct {
	RegistList []RegistListItem `json:"RegistList"`
}

type UnregisterSymbolAllResponse struct {
	RegistList []RegistListItem `json:"RegistList"`
}

func (c *KabuClient) RegisterSymbol(req RegisterSymbolRequest) (*RegisterSymbolResponse, error) {
	jsonData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("銘柄登録のJSON変換エラー: %v", err)
	}

	resp, err := c.doRequest("PUT", "/register", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("銘柄登録API通信エラー: %v", err)
	}

	var regResp RegisterSymbolResponse
	if err := c.DecodeResponse(resp, &regResp); err != nil {
		return nil, fmt.Errorf("銘柄登録失敗: %w", err)
	}

	return &regResp, nil
}

func (c *KabuClient) UnregisterSymbolAll() (*UnregisterSymbolAllResponse, error) {
	resp, err := c.doRequest("PUT", "/unregister/all", nil)
	if err != nil {
		return nil, fmt.Errorf("銘柄登録全解除API通信エラー: %v", err)
	}

	var regResp UnregisterSymbolAllResponse
	if err := c.DecodeResponse(resp, &regResp); err != nil {
		return nil, fmt.Errorf("銘柄登録全解除失敗: %w", err)
	}

	return &regResp, nil
}

type SymbolSuccess struct {
	Symbol          string  `json:"Symbol"`
	SymbolName      string  `json:"SymbolName"`
	PriceRangeGroup string  `json:"PriceRangeGroup"`
	UpperLimit      float64 `json:"UpperLimit"`
	LowerLimit      float64 `json:"LowerLimit"`
}

func (c *KabuClient) GetSymbol(symbol string, exchange ExchageType) (*SymbolSuccess, error) {
	endpoint := fmt.Sprintf("/symbol/%s@%d", symbol, exchange)
	resp, err := c.doRequest("GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("銘柄情報取得API通信エラー: %v", err)
	}

	var symbolResp SymbolSuccess
	if err := c.DecodeResponse(resp, &symbolResp); err != nil {
		return nil, fmt.Errorf("銘柄情報取得失敗: %w", err)
	}

	return &symbolResp, nil
}
