package api

type ExchageType int32

const (
	EXCHANGE_TYPE_TOSHO     ExchageType = 1
	EXCHANGE_TYPE_TOSHO_PLS ExchageType = 27
	EXCHANGE_TYPE_TOSHO_SOR ExchageType = 9
)

type Side string

const (
	SIDE_BUY  Side = "2"
	SIDE_SELL Side = "1"
)

// product: "0":すべて, "1":現物, "2":信用, "3":先物, "4":オプション
type ProductType string

const (
	ProductAll    ProductType = "0" // すべて (0)
	ProductCash                     // 現物（1）
	ProductMargin                   // 信用 (2)
	ProductFuture                   // 先物（3）
	ProductOption                   // オプション（4）
)
