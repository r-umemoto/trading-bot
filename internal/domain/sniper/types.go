package sniper

type ProductType int

const (
	ProductCash   ProductType = iota // 現物 (0)
	ProductMargin                    // 信用 (1)
)
