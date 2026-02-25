package kabu

import "github.com/r-umemoto/trading-bot/pkg/domain/market"

// トークン取得リクエスト用（こちらから送るデータ）
type TokenRequest struct {
	APIPassword string `json:"APIPassword"`
}

// トークン取得レスポンス用（APIから返ってくるデータ）
type TokenResponse struct {
	ResultCode int    `json:"ResultCode"`
	Token      string `json:"Token"`
}

// 板情報レスポンス用（APIから返ってくる価格データの一部）
// ※実際のレスポンスはもっと巨大ですが、スナイパーボットに必要なものだけ抜粋します
type BoardResponse struct {
	Symbol       string  `json:"Symbol"`       // 銘柄コード
	SymbolName   string  `json:"SymbolName"`   // 銘柄名
	CurrentPrice float64 `json:"CurrentPrice"` // 現在値

	// 最良売気配（一番安く売ってくれる人の価格と数量）
	Sell1 struct {
		Price float64 `json:"Price"`
		Qty   float64 `json:"Qty"`
	} `json:"Sell1"`

	// 最良買気配（一番高く買ってくれる人の価格と数量）
	Buy1 struct {
		Price float64 `json:"Price"`
		Qty   float64 `json:"Qty"`
	} `json:"Buy1"`
}

type PushMessage struct {
	Symbol       string  `json:"Symbol"`
	SymbolName   string  `json:"SymbolName"`
	CurrentPrice float64 `json:"CurrentPrice"`
	Time         string  `json:"Time"` // 約定時刻
	VWAP         float64 `json:"VWAP"`

	// ※実際のAPIからはさらに板の気配値なども大量に降ってきますが、
	// まずは現在値の監視に必要な項目だけ定義します。
}

// OrderRequest は新規・決済注文を発注するためのリクエストデータです
// https://kabucom.github.io/kabusapi/reference/index.html#operation/sendorderPost
type OrderRequest struct {
	Symbol             string  `json:"Symbol"`             // 銘柄コード (例: "9434")
	Exchange           int     `json:"Exchange"`           // 市場コード (1: 東証)
	SecurityType       int     `json:"SecurityType"`       // 商品種別 (1: 株式)
	Side               string  `json:"Side"`               // 売買区分 ("1": 売, "2": 買)
	CashMargin         int     `json:"CashMargin"`         // 信用区分 (1: 現物, 2: 信用新規, 3: 信用返済)
	MarginTradeType    int     `json:"MarginTradeType"`    // 信用取引区分 (1: 制度信用, 3: 一般信用デイトレ)
	AccountType        int     `json:"AccountType"`        // 口座種別 (4: 特定口座)
	Qty                float64 `json:"Qty"`                // 注文数量
	Price              float64 `json:"Price"`              // 注文価格 (0: 成行)
	ExpireDay          int     `json:"ExpireDay"`          // 注文有効期限 (0: 当日)
	FrontOrderType     int32   `json:"FrontOrderType"`     // 執行条件 (10: 成行, 20: 指値)
	DelivType          int32   `json:"DelivType"`          // 受渡区分 (0: 指定なし, 2: お預かり金, 3: Auマネーコネクト)
	ClosePositionOrder int32   `json:"ClosePositionOrder"` // 決済順序
}

// OrderResponse は発注後のレスポンスデータです
type OrderResponse struct {
	Result  int    `json:"Result"`  // 結果コード (0: 成功)
	OrderId string `json:"OrderId"` // 受付番号 (注文の追跡やキャンセルに使う超重要ID)
}

// Position は1つの建玉（現在保有しているポジション）を表します
type Position struct {
	ExecutionID     string  `json:"ExecutionID"`     // 約定番号（決済指定時に使う）
	Exchange        int32   `json:"Exchange"`        //
	AccountType     int32   `json:"AccountType"`     //
	MarginTradeType int32   `json:"MarginTradeType"` //
	Side            string  `json:"Side"`            //
	Symbol          string  `json:"Symbol"`          // 銘柄コード (例: "7012")
	SymbolName      string  `json:"SymbolName"`      // 銘柄名
	LeavesQty       float64 `json:"LeavesQty"`       // 残数量（いま決済できる株数）
	HoldQty         float64 `json:"HoldQty"`         // 拘束数量（すでに売り注文を出して待機中の株数）
	Price           float64 `json:"Price"`           // 建値（平均取得単価） ★0.2%計算の基準！
	CurrentPrice    float64 `json:"CurrentPrice"`    // 現在値
	Valuation       float64 `json:"Valuation"`       // 評価金額
	ProfitLoss      float64 `json:"ProfitLoss"`      // 評価損益
	ProfitLossRate  float64 `json:"ProfitLossRate"`  // 評価損益率
}

// ※kabuステーションAPIの /positions は、このオブジェクトの「配列」を返してきます。

// CancelRequest は注文取消用のリクエストデータです
type CancelRequest struct {
	OrderID string `json:"OrderId"` // 取消したい注文の受付番号
}

// CancelResponse は注文取消後のレスポンスデータです
type CancelResponse struct {
	Result  int    `json:"Result"`
	OrderID string `json:"OrderId"`
}

type Side string

const (
	SIDE_BUY  Side = "2"
	SIDE_SELL Side = "1"
)

func (s Side) print() string {
	switch s {
	case SIDE_BUY:
		return "Buy"
	case SIDE_SELL:
		return "Sell"
	default:
		return "unknown"
	}
}

func (s Side) toAction() market.Action {
	switch s {
	case SIDE_BUY:
		return market.ACTION_BUY
	case SIDE_SELL:
		return market.ACTION_SELL
	default:
		return "unknown"
	}
}

// product: "0":すべて, "1":現物, "2":信用, "3":先物, "4":オプション
type ProductType string

const (
	ProductAll    ProductType = "0" // すべて (0)
	ProductCash                     // 現物（1）
	ProductMargin                   // 信用 (2)
	ProductFuture                   // 先物（3）
	ProductOption                   // オプション（4）
)

type Execution struct {
	ID    string  `json:"ExecutionID"` // 注文ID（キャンセル時に必要）
	Price float64 `json:"Price"`       // 約定値段
	Qty   float64 `json:"Qty"`         // 約定数量
}

type Order struct {
	ID       string      `json:"ID"`       // 注文ID（キャンセル時に必要）
	State    int32       `json:"State"`    // 状態（3: 処理中/待機中, 5: 終了 など）
	Symbol   string      `json:"Symbol"`   // 銘柄コード
	Side     Side        `json:"Side"`     // 売買区分
	OrderQty float64     `json:"OrderQty"` // 発注数量
	CumQty   float64     `json:"CumQty"`   // 約定数量
	Price    float64     `json:"Price"`    // 値段
	Details  []Execution `json:"Details"`  // 値段
}
