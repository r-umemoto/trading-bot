package engine

// TradeAction は売買の方向を示します
type TradeAction string

const (
	ActionBuy  TradeAction = "BUY"
	ActionSell TradeAction = "SELL"
	ActionHold TradeAction = "HOLD" // 何もしない
)

// TradeSignal は戦略がスナイパーに渡す「命令書」です
type TradeSignal struct {
	Action   TradeAction
	Symbol   string
	Price    float64 // 指値の価格（成行の場合は0）
	Quantity int     // 注文数量
}

// Strategy はすべての売買ロジックが満たすべき規格です
type Strategy interface {
	Evaluate(currentPrice float64) TradeSignal
}

// --- 以下、具体的な戦略の実装 ---

// FixedRateStrategy は指定した利率で利確（売り）を判断するシンプルな戦略です
type FixedRateStrategy struct {
	Symbol      string
	EntryPrice  float64
	TargetRate  float64
	TargetPrice float64
	Quantity    int
	HasFired    bool // 1つの戦略につき1回だけ発動させるための状態
}

// NewFixedRateStrategy は新しい固定利率戦略を生成します
func NewFixedRateStrategy(symbol string, entryPrice, targetRate float64, qty int) *FixedRateStrategy {
	return &FixedRateStrategy{
		Symbol:      symbol,
		EntryPrice:  entryPrice,
		TargetRate:  targetRate,
		TargetPrice: entryPrice * (1.0 + targetRate),
		Quantity:    qty,
		HasFired:    false,
	}
}

// Evaluate は価格を受け取り、条件を満たせば「売り」の命令書を出します
func (s *FixedRateStrategy) Evaluate(currentPrice float64) TradeSignal {
	if s.HasFired {
		return TradeSignal{Action: ActionHold}
	}

	if currentPrice >= s.TargetPrice {
		s.HasFired = true // 2重発注防止
		return TradeSignal{
			Action:   ActionSell,
			Symbol:   s.Symbol,
			Price:    0, // 成行決済
			Quantity: s.Quantity,
		}
	}

	return TradeSignal{Action: ActionHold}
}
