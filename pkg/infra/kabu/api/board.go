package api

import (
	"encoding/json"
	"fmt"
	"time"
)

// Quote は板の各気配情報（値段と数量）を表します
type Quote struct {
	Price float64 `json:"Price"`
	Qty   float64 `json:"Qty"`
}

// FirstQuote は最良気配（1本目）の情報（値段、数量、時刻、フラグ）を表します
type FirstQuote struct {
	Price float64   `json:"Price"`
	Qty   float64   `json:"Qty"`
	Time  time.Time `json:"Time"`
	Sign  string    `json:"Sign"`
}

// BoardResponse は時価情報・板情報レスポンスを表します
// REST API (/board) および PUSH配信の両方で共通の構造です
type BoardResponse struct {
	// 銘柄情報
	Symbol       string `json:"Symbol"`       // 銘柄コード
	SymbolName   string `json:"SymbolName"`   // 銘柄名
	Exchange     int    `json:"Exchange"`     // 市場コード
	ExchangeName string `json:"ExchangeName"` // 市場名称
	SecurityType int    `json:"SecurityType"` // 銘柄種別

	// 現在値情報
	CurrentPrice             float64   `json:"CurrentPrice"`             // 現値
	CurrentPriceTime         time.Time `json:"CurrentPriceTime"`         // 現値時刻
	CurrentPriceChangeStatus string    `json:"CurrentPriceChangeStatus"` // 現値前値比較
	CurrentPriceStatus       int       `json:"CurrentPriceStatus"`       // 現値ステータス
	CalcPrice                float64   `json:"CalcPrice"`                // 計算用現値

	// 前日比・騰落率
	PreviousClose          float64   `json:"PreviousClose"`          // 前日終値
	PreviousCloseTime      time.Time `json:"PreviousCloseTime"`      // 前日終値日付
	ChangePreviousClose    float64   `json:"ChangePreviousClose"`    // 前日比
	ChangePreviousClosePer float64   `json:"ChangePreviousClosePer"` // 騰落率

	// 四本値
	OpeningPrice     float64   `json:"OpeningPrice"`     // 始値
	OpeningPriceTime time.Time `json:"OpeningPriceTime"` // 始値時刻
	HighPrice        float64   `json:"HighPrice"`        // 高値
	HighPriceTime    time.Time `json:"HighPriceTime"`    // 高値時刻
	LowPrice         float64   `json:"LowPrice"`         // 安値
	LowPriceTime     time.Time `json:"LowPriceTime"`     // 安値時刻

	// 売買高・売買代金
	TradingVolume     float64   `json:"TradingVolume"`     // 売買高
	TradingVolumeTime time.Time `json:"TradingVolumeTime"` // 売買高時刻
	VWAP              float64   `json:"VWAP"`              // 売買高加重平均価格（VWAP）
	TradingValue      float64   `json:"TradingValue"`      // 売買代金

	// 最良気配 (Bid/Ask) - トレーダー目線
	// BidPrice=Sell1のPrice、AskPrice=Buy1のPriceとなります
	BidQty   float64   `json:"BidQty"`   // 最良売気配数量
	BidPrice float64   `json:"BidPrice"` // 最良売気配値段
	BidTime  time.Time `json:"BidTime"`  // 最良売気配時刻
	BidSign  string    `json:"BidSign"`  // 最良売気配フラグ
	AskQty   float64   `json:"AskQty"`   // 最良買気配数量
	AskPrice float64   `json:"AskPrice"` // 最良買気配値段
	AskTime  time.Time `json:"AskTime"`  // 最良買気配時刻
	AskSign  string    `json:"AskSign"`  // 最良買気配フラグ

	// 板情報 (売気配)
	Sell1  FirstQuote `json:"Sell1"`
	Sell2  Quote      `json:"Sell2"`
	Sell3  Quote      `json:"Sell3"`
	Sell4  Quote      `json:"Sell4"`
	Sell5  Quote      `json:"Sell5"`
	Sell6  Quote      `json:"Sell6"`
	Sell7  Quote      `json:"Sell7"`
	Sell8  Quote      `json:"Sell8"`
	Sell9  Quote      `json:"Sell9"`
	Sell10 Quote      `json:"Sell10"`

	// 板情報 (買気配)
	Buy1  FirstQuote `json:"Buy1"`
	Buy2  Quote      `json:"Buy2"`
	Buy3  Quote      `json:"Buy3"`
	Buy4  Quote      `json:"Buy4"`
	Buy5  Quote      `json:"Buy5"`
	Buy6  Quote      `json:"Buy6"`
	Buy7  Quote      `json:"Buy7"`
	Buy8  Quote      `json:"Buy8"`
	Buy9  Quote      `json:"Buy9"`
	Buy10 Quote      `json:"Buy10"`

	// 集計・その他
	MarketOrderSellQty float64 `json:"MarketOrderSellQty"` // 売成行数量
	MarketOrderBuyQty  float64 `json:"MarketOrderBuyQty"`  // 買成行数量
	OverSellQty        float64 `json:"OverSellQty"`        // OVER気配数量
	UnderBuyQty        float64 `json:"UnderBuyQty"`        // UNDER気配数量
	TotalMarketValue   float64 `json:"TotalMarketValue"`   // 時価総額

	// デリバティブ関連
	ClearingPrice float64 `json:"ClearingPrice"` // 清算値
	IV            float64 `json:"IV"`            // インプライド・ボラティリティ
	Gamma         float64 `json:"Gamma"`         // ガンマ
	Theta         float64 `json:"Theta"`         // セータ
	Vega          float64 `json:"Vega"`          // ベガ
	Delta         float64 `json:"Delta"`         // デルタ
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
