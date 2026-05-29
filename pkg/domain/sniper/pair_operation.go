package sniper

import (
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/brain"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

// InstructionStrategy は、司令官 (Commander) からの指示をそのまま実行する Sniper 用の戦略実装です。
type InstructionStrategy struct {
	mu            sync.Mutex
	pendingSignal brain.Signal
}

func NewInstructionStrategy() *InstructionStrategy {
	return &InstructionStrategy{
		pendingSignal: brain.Signal{Action: brain.ACTION_HOLD},
	}
}

func (inst *InstructionStrategy) Name() string {
	return "InstructionStrategy"
}

func (inst *InstructionStrategy) Evaluate(input strategy.StrategyInput) brain.Signal {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	sig := inst.pendingSignal
	inst.pendingSignal = brain.Signal{Action: brain.ACTION_HOLD} // 評価後にリセット
	return sig
}

func (inst *InstructionStrategy) IfDone(input strategy.StrategyInput, prevSignal brain.Signal) brain.Signal {
	return brain.Signal{Action: brain.ACTION_HOLD}
}

func (inst *InstructionStrategy) AnalysisLogger() *slog.Logger {
	return nil
}

func (inst *InstructionStrategy) ShouldCancel(input strategy.StrategyInput, ord *order.Order) bool {
	return false
}

func (inst *InstructionStrategy) SetSignal(sig brain.Signal) {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	inst.pendingSignal = sig
}

// PairTradingOperation は、2つの銘柄（Nest A と Nest B）を監視し、
// スプレッド（価格差）の乖離をトリガーに両建て注文を同期執行する作戦（Operation）です。
type PairTradingOperation struct {
	ID                 string
	nestA              *SniperNest
	nestB              *SniperNest
	strategyA          *InstructionStrategy
	strategyB          *InstructionStrategy
	dataPool           tick.DataPool
	thresholdPriceDiff float64 // スプレッドの閾値
	tradeQty           float64 // 取引数量
	logger             *slog.Logger
}

func NewPairTradingOperation(
	id string,
	nestA *SniperNest,
	nestB *SniperNest,
	strategyA *InstructionStrategy,
	strategyB *InstructionStrategy,
	dataPool tick.DataPool,
	threshold float64,
	qty float64,
	logger *slog.Logger,
) *PairTradingOperation {
	if logger == nil {
		logger = slog.Default()
	}
	return &PairTradingOperation{
		ID:                 id,
		nestA:              nestA,
		nestB:              nestB,
		strategyA:          strategyA,
		strategyB:          strategyB,
		dataPool:           dataPool,
		thresholdPriceDiff: threshold,
		tradeQty:           qty,
		logger:             logger,
	}
}

// Operation インターフェースのメソッド実装群

func (o *PairTradingOperation) GetID() string {
	return o.ID
}

func (o *PairTradingOperation) GetSymbolCode() string {
	// 代表として銘柄Aのコードを返す（互換性用）
	return o.nestA.SymbolCode
}

func (o *PairTradingOperation) GetSymbolCodes() []string {
	return []string{o.nestA.SymbolCode, o.nestB.SymbolCode}
}

func (o *PairTradingOperation) GetExchanges() []order.ExchangeMarket {
	seen := make(map[order.ExchangeMarket]bool)
	var list []order.ExchangeMarket
	for _, nest := range []*SniperNest{o.nestA, o.nestB} {
		for _, ex := range nest.GetExchanges() {
			if !seen[ex] {
				seen[ex] = true
				list = append(list, ex)
			}
		}
	}
	return list
}

func (o *PairTradingOperation) HandleTick(t tick.Tick) []FireAction {
	// 1. 最新価格の取得
	stateA := o.dataPool.GetState(o.nestA.SymbolCode)
	stateB := o.dataPool.GetState(o.nestB.SymbolCode)

	if stateA.LatestTick.CurrentPriceTime.IsZero() || stateB.LatestTick.CurrentPriceTime.IsZero() {
		return nil
	}

	priceA := stateA.LatestTick.Price
	priceB := stateB.LatestTick.Price
	priceDiff := priceA - priceB

	o.logger.Info("PAIR_SPREAD_MONITOR",
		slog.String("operation", o.ID),
		slog.Float64("price_a", priceA),
		slog.Float64("price_b", priceB),
		slog.Float64("spread", priceDiff),
	)

	// 2. 現在の保有状況の確認
	qtyA := o.nestA.spotter.HoldQty(o.nestA.snipers[0].ID)
	qtyB := o.nestB.spotter.HoldQty(o.nestB.snipers[0].ID)

	var actions []FireAction

	// 3. 取引シグナルの生成
	if qtyA == 0 && qtyB == 0 {
		// ノーポジションのとき、スプレッド乖離を判定してエントリー
		if priceDiff > o.thresholdPriceDiff {
			o.logger.Warn("PAIR_ENTRY_SIGNAL_DETECTED", slog.String("reason", "spread_exceeded_positive_threshold"))
			// 銘柄Aを売り、銘柄Bを買う
			o.strategyA.SetSignal(brain.Signal{Action: brain.ACTION_SELL, Price: priceA, Quantity: o.tradeQty, Reason: "PairEntry_SellA"})
			o.strategyB.SetSignal(brain.Signal{Action: brain.ACTION_BUY, Price: priceB, Quantity: o.tradeQty, Reason: "PairEntry_BuyB"})
		} else if priceDiff < -o.thresholdPriceDiff {
			o.logger.Warn("PAIR_ENTRY_SIGNAL_DETECTED", slog.String("reason", "spread_exceeded_negative_threshold"))
			// 銘柄Aを買い、銘柄Bを売る
			o.strategyA.SetSignal(brain.Signal{Action: brain.ACTION_BUY, Price: priceA, Quantity: o.tradeQty, Reason: "PairEntry_BuyA"})
			o.strategyB.SetSignal(brain.Signal{Action: brain.ACTION_SELL, Price: priceB, Quantity: o.tradeQty, Reason: "PairEntry_SellB"})
		}
	} else {
		// ポジションを保有している場合、平均回帰したら手仕舞い（利確/損切）
		// スプレッドの絶対値が元の閾値の10%未満に収束したら決済
		if math.Abs(priceDiff) < o.thresholdPriceDiff*0.1 {
			o.logger.Warn("PAIR_EXIT_SIGNAL_DETECTED", slog.String("reason", "spread_reverted_to_mean"))
			if qtyA > 0 {
				o.strategyA.SetSignal(brain.Signal{Action: brain.ACTION_SELL, Price: priceA, Quantity: math.Abs(qtyA), Reason: "PairExit_SellA"})
				o.strategyB.SetSignal(brain.Signal{Action: brain.ACTION_BUY, Price: priceB, Quantity: math.Abs(qtyB), Reason: "PairExit_BuyB"})
			} else {
				o.strategyA.SetSignal(brain.Signal{Action: brain.ACTION_BUY, Price: priceA, Quantity: math.Abs(qtyA), Reason: "PairExit_BuyA"})
				o.strategyB.SetSignal(brain.Signal{Action: brain.ACTION_SELL, Price: priceB, Quantity: math.Abs(qtyB), Reason: "PairExit_SellB"})
			}
		}
	}

	// 4. 2つの SniperNest にTick処理を伝達し、個別のアクション結果をマージして返却
	actA := o.nestA.HandleTick(stateA.LatestTick)
	actB := o.nestB.HandleTick(stateB.LatestTick)

	actions = append(actions, actA...)
	actions = append(actions, actB...)

	return actions
}

func (o *PairTradingOperation) UpdateOrders(report order.Orders) {
	o.nestA.UpdateOrders(report)
	o.nestB.UpdateOrders(report)
}

func (o *PairTradingOperation) ForceExit() {
	o.nestA.ForceExit()
	o.nestB.ForceExit()
}

func (o *PairTradingOperation) GetActiveOrders() []*order.Order {
	var all []*order.Order
	all = append(all, o.nestA.GetActiveOrders()...)
	all = append(all, o.nestB.GetActiveOrders()...)
	return all
}

func (o *PairTradingOperation) GetReportableTargets() []ReportableTarget {
	var all []ReportableTarget
	all = append(all, o.nestA.GetReportableTargets()...)
	all = append(all, o.nestB.GetReportableTargets()...)
	return all
}

func (o *PairTradingOperation) HasSniper(sniperID string) bool {
	return o.nestA.HasSniper(sniperID) || o.nestB.HasSniper(sniperID)
}

func (o *PairTradingOperation) FailSendingOrder(sniperID string, ord *order.Order) {
	if o.nestA.HasSniper(sniperID) {
		o.nestA.FailSendingOrder(sniperID, ord)
	} else if o.nestB.HasSniper(sniperID) {
		o.nestB.FailSendingOrder(sniperID, ord)
	}
}

func (o *PairTradingOperation) UpdateOrderID(sniperID string, ord *order.Order, newID string) {
	if o.nestA.HasSniper(sniperID) {
		o.nestA.UpdateOrderID(sniperID, ord, newID)
	} else if o.nestB.HasSniper(sniperID) {
		o.nestB.UpdateOrderID(sniperID, ord, newID)
	}
}

func (o *PairTradingOperation) GetPerformance(sniperID string) Performance {
	if o.nestA.HasSniper(sniperID) {
		return o.nestA.GetPerformance(sniperID)
	} else if o.nestB.HasSniper(sniperID) {
		return o.nestB.GetPerformance(sniperID)
	}
	return Performance{}
}

func (o *PairTradingOperation) GetUnrealizedPnL(sniperID string, currentPrice float64) float64 {
	if o.nestA.HasSniper(sniperID) {
		return o.nestA.GetUnrealizedPnL(sniperID, currentPrice)
	} else if o.nestB.HasSniper(sniperID) {
		return o.nestB.GetUnrealizedPnL(sniperID, currentPrice)
	}
	return 0
}

// pairTradingStrategyFactory は、portfolio.json から "pair_trading" 戦略として指定された際に
// deploySnipers が正常に動作するためのファクトリ実装です。
type pairTradingStrategyFactory struct{}

func (f *pairTradingStrategyFactory) NewStrategy(detail symbol.Symbol, dataPool tick.DataPool, params interface{}) strategy.Strategy {
	return NewInstructionStrategy()
}

func (f *pairTradingStrategyFactory) CreateExecutionPolicy(params interface{}) strategy.ExecutionPolicy {
	// ペアトレードは成り行き（または指値）などを即座に約定推測するために、
	// TouchTTLPolicy（TTL: 2秒）を利用します。
	return &strategy.TouchTTLPolicy{TTL: 2000 * time.Millisecond}
}

func init() {
	strategy.Register("pair_trading", &pairTradingStrategyFactory{})
}
