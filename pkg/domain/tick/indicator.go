package tick

// Indicator は各種計算指標が満たすべきインターフェースです。
type Indicator interface {
	// ID はこの指標の一意識別子を返します（例："SMA_75", "VWAP_5min"）
	ID() string
	// Update は新しいTickデータを受け取り、内部状態を更新します
	Update(tick Tick)
	// Dependencies は、この指標が依存している他の指標のリストを返します（無い場合はnil）
	Dependencies() []Indicator
}

// HistoricalFeeder は、指定された期間の日足SMAなどのヒストリカル値を取得するインターフェースです。
type HistoricalFeeder interface {
	FetchSMA(period int) (float64, error)
}

// HistoricalFeederProvider は、銘柄ごとの HistoricalFeeder を提供するインターフェースです。
type HistoricalFeederProvider interface {
	GetFeeder(symbol string) HistoricalFeeder
}

// FetcherIndicator は、初期化時にヒストリカルデータのフェッチを必要とする指標が実装するインターフェースです。
type FetcherIndicator interface {
	Indicator
	FetchAndInitialize(feeder HistoricalFeeder) error
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

func (i *StaticFloatIndicator) Dependencies() []Indicator {
	return nil
}
