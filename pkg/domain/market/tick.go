package market

import "time"

// Quote は板の各気配情報（値段と数量）を表します
type Quote struct {
	Price float64
	Qty   float64
}

// FirstQuote は最良気配の情報（値段、数量、時刻、フラグ）を表します
type FirstQuote struct {
	Price float64
	Qty   float64
	Time  time.Time
	Sign  string
}

// Tick 価格変動で発生するイベント（エンティティ）
type Tick struct {
	Symbol           string
	Price            float64
	VWAP             float64
	TradingVolume    float64   // 売買高
	CurrentPriceTime time.Time // 現値時刻

	// 最良気配 (売り・買い)
	BestAsk FirstQuote // 最良売気配 (Sell1)
	BestBid FirstQuote // 最良買気配 (Buy1)

	// 板情報 (10本目まで)
	SellBoard []Quote
	BuyBoard  []Quote

	// 現在値ステータス・比較
	CurrentPriceStatus       PriceStatus       // 現値ステータス
	CurrentPriceChangeStatus PriceChangeStatus // 現値前値比較

	// 四本値・集計
	OpeningPrice       float64 // 始値
	TradingValue       float64 // 売買代金
	MarketOrderSellQty float64 // 売成行数量
	MarketOrderBuyQty  float64 // 買成行数量
	OverSellQty        float64 // OVER気配数量
	UnderBuyQty        float64 // UNDER気配数量
}

// IsExecution は「今、取引が行われたか」を判定します
func (t Tick) IsExecution() bool {
	// 抽象化されたステータスに基づいて判定
	statusMatch := t.CurrentPriceStatus == PRICE_STATUS_CURRENT ||
		t.CurrentPriceStatus == PRICE_STATUS_OPENING ||
		t.CurrentPriceStatus == PRICE_STATUS_PRE_CLOSE ||
		t.CurrentPriceStatus == PRICE_STATUS_CLOSE

	// 価格と出来高が正であることも含めて「約定」と定義する
	return statusMatch && t.Price > 0 && t.TradingVolume > 0
}

// MarketState は、特定銘柄の「今の市場環境」の生データを保持するエンティティです
type MarketState struct {
	Symbol     string
	LatestTick Tick // 最新のTickデータ
}

func NewTick(
	symbol string,
	price float64,
	vwap float64,
	tradingVolume float64,
	currentPriceTime time.Time,
	bestAsk FirstQuote,
	bestBid FirstQuote,
	sellBoard []Quote,
	buyBoard []Quote,
	currentPriceStatus PriceStatus,
	currentPriceChangeStatus PriceChangeStatus,
	openingPrice float64,
	tradingValue float64,
	marketOrderSellQty float64,
	marketOrderBuyQty float64,
	overSellQty float64,
	underBuyQty float64,
) Tick {
	return Tick{
		Symbol:                   symbol,
		Price:                    price,
		VWAP:                     vwap,
		TradingVolume:            tradingVolume,
		CurrentPriceTime:         currentPriceTime,
		BestAsk:                  bestAsk,
		BestBid:                  bestBid,
		SellBoard:                sellBoard,
		BuyBoard:                 buyBoard,
		CurrentPriceStatus:       currentPriceStatus,
		CurrentPriceChangeStatus: currentPriceChangeStatus,
		OpeningPrice:             openingPrice,
		TradingValue:             tradingValue,
		MarketOrderSellQty:       marketOrderSellQty,
		MarketOrderBuyQty:        marketOrderBuyQty,
		OverSellQty:              overSellQty,
		UnderBuyQty:              underBuyQty,
	}
}
