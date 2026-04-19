package market

// Indicator は各種計算指標が満たすべきインターフェースです。
type Indicator interface {
	// ID はこの指標の一意識別子を返します（例："SMA_75", "VWAP_5min"）
	ID() string
	// Update は新しいTickデータを受け取り、内部状態を更新します
	Update(tick Tick)
}

// StaticFloatIndicator は、外部から値をセットされる静的な指標です（例: 前日からのSMAなど、Tickで更新されないもの）
type StaticFloatIndicator struct {
	id    string
	value float64
}

func NewStaticFloatIndicator(id string, initialValue float64) *StaticFloatIndicator {
	return &StaticFloatIndicator{
		id:    id,
		value: initialValue,
	}
}

func (i *StaticFloatIndicator) ID() string {
	return i.id
}

func (i *StaticFloatIndicator) Update(tick Tick) {
	// 静的指標なのでTickでは更新されない
}

func (i *StaticFloatIndicator) Value() interface{} {
	return i.value
}

func (i *StaticFloatIndicator) SetValue(val float64) {
	i.value = val
}
