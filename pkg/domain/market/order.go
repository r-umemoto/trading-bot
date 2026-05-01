package market

type OrderStatus uint32

const (
	ORDER_STATUS_NONE        OrderStatus = 0
	ORDER_STATUS_WAITING     OrderStatus = 1 // 発注受付中・取引所送信前
	ORDER_STATUS_IN_PROGRESS OrderStatus = 2 // 取引所にて執行中（一部約定を含む）
	ORDER_STATUS_FILLED      OrderStatus = 3 // 全約定（完了）
	ORDER_STATUS_CANCELED    OrderStatus = 4 // 取消済（完了）
	ORDER_STATUS_EXPIRED     OrderStatus = 5 // 失効・期限切れ（完了）
)

// Execution は1回の約定の事実を表す値オブジェクトです
type Execution struct {
	ID    string
	Price float64
	Qty   float64
	// 必要に応じて約定日時なども持たせます
}

// Order は注文全体を管理する集約ルート（エンティティ）です
type Order struct {
	ID         string
	Symbol     string
	Action     Action
	OrderPrice float64 // 発注時の指値（成行の場合は0など）
	OrderQty   float64 // 発注した総数量

	Executions []Execution // 🌟 約定のコレクション

	Status OrderStatus // 注文の状態
}

func NewOrder(id string, symbol string, action Action, price float64, qty float64) Order {
	return Order{
		ID:         id,
		Symbol:     symbol,
		Action:     action,
		OrderPrice: price,
		OrderQty:   qty,
		Status:     ORDER_STATUS_WAITING,
	}
}

// FilledQty は現在までに約定した合計数量を返します
func (o *Order) FilledQty() float64 {
	var sum float64
	for _, exec := range o.Executions {
		sum += exec.Qty
	}
	return sum
}

// AveragePrice は約定済みの平均単価を返します（未約定の場合は0）
func (o *Order) AveragePrice() float64 {
	if len(o.Executions) == 0 {
		return 0.0
	}
	var totalCost float64
	var totalQty float64
	for _, exec := range o.Executions {
		totalCost += exec.Price * float64(exec.Qty)
		totalQty += exec.Qty
	}
	if totalQty == 0 {
		return 0.0
	}
	return totalCost / float64(totalQty)
}

// IsCompleted は注文が完全に終了したか（全約定 or キャンセル or 期限切れ）を判定します
func (o *Order) IsCompleted() bool {
	return o.Status == ORDER_STATUS_FILLED || o.Status == ORDER_STATUS_CANCELED || o.Status == ORDER_STATUS_EXPIRED
}

// HasExecution は指定された約定IDが既に存在するかを判定します
func (o *Order) HasExecution(execID string) bool {
	for _, exec := range o.Executions {
		if exec.ID == execID {
			return true
		}
	}
	return false
}

// AddExecution は新しい約定を追加します（重複チェック付き）
func (o *Order) AddExecution(exec Execution) {
	// 既に同じ約定IDが存在すれば無視（冪等性の担保）
	for _, existing := range o.Executions {
		if existing.ID == exec.ID {
			return
		}
	}
	o.Executions = append(o.Executions, exec)
}
