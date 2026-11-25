package sniper

import (
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/position"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

// InstructionStrategy は、司令官 (Commander) からの指示をそのまま実行する Sniper 用の戦略実装です。
type InstructionStrategy struct {
	mu        sync.Mutex
	targetPos strategy.TargetPosition
}

func NewInstructionStrategy() *InstructionStrategy {
	return &InstructionStrategy{
		targetPos: strategy.TargetPosition{Qty: 0},
	}
}

func (inst *InstructionStrategy) Name() string {
	return "InstructionStrategy"
}

func (inst *InstructionStrategy) Evaluate(input strategy.StrategyInput) strategy.TargetPosition {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	return inst.targetPos
}

func (inst *InstructionStrategy) AnalysisLogger() *slog.Logger {
	return nil
}

func (inst *InstructionStrategy) SetTarget(target strategy.TargetPosition) {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	inst.targetPos = target
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

var pairTradingJST = time.FixedZone("JST", 9*60*60)

const defaultPairStopLossMultiplier = 2.5

func (o *PairTradingOperation) isAllowedTimeForEntry(t time.Time) bool {
	// 日本時間 (JST) に統一して時間判定する
	t = t.In(pairTradingJST)

	hm := t.Hour()*100 + t.Minute()

	// 前場安定期: 09:30 〜 11:30
	// 後場安定期: 12:45 〜 14:45
	if (hm >= 930 && hm < 1130) || (hm >= 1245 && hm < 1445) {
		return true
	}
	return false
}

func (o *PairTradingOperation) isEODForceExitTime(t time.Time) bool {
	if t.IsZero() {
		return false
	}
	t = t.In(pairTradingJST)
	hm := t.Hour()*100 + t.Minute()
	return hm >= 1450
}

func (o *PairTradingOperation) HandleTick(t tick.Tick) []FireAction {
	// 1. 最新価格の取得
	stateA := o.dataPool.GetState(o.nestA.SymbolCode)
	stateB := o.dataPool.GetState(o.nestB.SymbolCode)

	if stateA.LatestTick.CurrentPriceTime.IsZero() || stateB.LatestTick.CurrentPriceTime.IsZero() {
		return nil
	}

	// 最新時刻は受信TickまたはA/Bのうち新しい方を採用（片方の板が閑散なときの時刻ズレを防止）
	now := t.CurrentPriceTime
	if now.IsZero() {
		now = stateA.LatestTick.CurrentPriceTime
		if stateB.LatestTick.CurrentPriceTime.After(now) {
			now = stateB.LatestTick.CurrentPriceTime
		}
	}

	priceA := stateA.LatestTick.Price
	priceB := stateB.LatestTick.Price

	// 始値（OpeningPrice）を基準価格とする。未設定の場合は最新価格でフォールバック。
	openA := stateA.LatestTick.OpeningPrice
	if openA == 0 {
		openA = priceA
	}
	openB := stateB.LatestTick.OpeningPrice
	if openB == 0 {
		openB = priceB
	}

	normA := priceA / openA
	normB := priceB / openB
	priceDiff := normA - normB

	o.logger.Info("PAIR_SPREAD_MONITOR",
		slog.String("operation", o.ID),
		slog.Float64("price_a", priceA),
		slog.Float64("price_b", priceB),
		slog.Float64("spread", priceDiff),
	)

	// 2. 現在の保有状況の確認
	qtyA := o.nestA.HoldQty(o.nestA.snipers[0].ID)
	qtyB := o.nestB.HoldQty(o.nestB.snipers[0].ID)

	var actions []FireAction

	// 3. 金額等価になるように数量をスケーリング (100株単位に丸める)
	qtyA_scaled := o.tradeQty
	qtyB_scaled := o.tradeQty

	if openA < openB {
		ratio := openB / openA
		qtyA_scaled = math.Round(o.tradeQty*ratio/100.0) * 100.0
		if qtyA_scaled < 100.0 {
			qtyA_scaled = 100.0
		}
	} else {
		ratio := openA / openB
		qtyB_scaled = math.Round(o.tradeQty*ratio/100.0) * 100.0
		if qtyB_scaled < 100.0 {
			qtyB_scaled = 100.0
		}
	}

	// 4. 取引シグナルの生成
	if qtyA == 0 && qtyB == 0 {
		// ノーポジションのとき、スプレッド乖離を判定してエントリー
		// 新規エントリー時のみ時間帯フィルターを適用する
		if o.isAllowedTimeForEntry(now) {
			if priceDiff > o.thresholdPriceDiff {
				o.logger.Warn("PAIR_ENTRY_SIGNAL_DETECTED", slog.String("reason", "spread_exceeded_positive_threshold"))
				// 銘柄Aを売り、銘柄Bを買う
				o.strategyA.SetTarget(strategy.TargetPosition{Qty: -qtyA_scaled, Price: 0.0, OrderType: order.ORDER_TYPE_MARKET, Reason: "PairEntry_SellA"})
				o.strategyB.SetTarget(strategy.TargetPosition{Qty: qtyB_scaled, Price: 0.0, OrderType: order.ORDER_TYPE_MARKET, Reason: "PairEntry_BuyB"})
			} else if priceDiff < -o.thresholdPriceDiff {
				o.logger.Warn("PAIR_ENTRY_SIGNAL_DETECTED", slog.String("reason", "spread_exceeded_negative_threshold"))
				// 銘柄Aを買い、銘柄Bを売る
				o.strategyA.SetTarget(strategy.TargetPosition{Qty: qtyA_scaled, Price: 0.0, OrderType: order.ORDER_TYPE_MARKET, Reason: "PairEntry_BuyA"})
				o.strategyB.SetTarget(strategy.TargetPosition{Qty: -qtyB_scaled, Price: 0.0, OrderType: order.ORDER_TYPE_MARKET, Reason: "PairEntry_SellB"})
			}
		} else {
			if math.Abs(priceDiff) > o.thresholdPriceDiff {
				o.logger.Info("PAIR_ENTRY_SKIPPED_BY_TIME_FILTER",
					slog.String("reason", "outside_golden_time_windows"),
					slog.Time("tick_time", now),
				)
			}
		}
	} else if (qtyA != 0) != (qtyB != 0) {
		// 片足（Legging）状態の検知：片方のみ約定し、もう一方にアクティブ注文もない場合は裸ポジションを即時解消する
		missingOrders := o.nestB.GetActiveOrders()
		if qtyA == 0 {
			missingOrders = o.nestA.GetActiveOrders()
		}
		if len(missingOrders) == 0 {
			o.logger.Warn("PAIR_LEGGING_UNWIND_TRIGGERED",
				slog.String("operation", o.ID),
				slog.Float64("qty_a", qtyA),
				slog.Float64("qty_b", qtyB),
			)
			o.strategyA.SetTarget(strategy.TargetPosition{Qty: 0.0, Price: 0.0, OrderType: order.ORDER_TYPE_MARKET, Reason: "PairUnwind_LeggingA"})
			o.strategyB.SetTarget(strategy.TargetPosition{Qty: 0.0, Price: 0.0, OrderType: order.ORDER_TYPE_MARKET, Reason: "PairUnwind_LeggingB"})
		}
	} else {
		// 両建て保有中：平均回帰（利確）、スプレッド拡大ストップロス、または大引け前強制手仕舞いを判定
		if math.Abs(priceDiff) < o.thresholdPriceDiff*0.1 {
			o.logger.Warn("PAIR_EXIT_SIGNAL_DETECTED", slog.String("reason", "spread_reverted_to_mean"))
			o.strategyA.SetTarget(strategy.TargetPosition{Qty: 0.0, Price: 0.0, OrderType: order.ORDER_TYPE_MARKET, Reason: "PairExit_FlatA"})
			o.strategyB.SetTarget(strategy.TargetPosition{Qty: 0.0, Price: 0.0, OrderType: order.ORDER_TYPE_MARKET, Reason: "PairExit_FlatB"})
		} else if o.thresholdPriceDiff > 0 && math.Abs(priceDiff) >= o.thresholdPriceDiff*defaultPairStopLossMultiplier {
			o.logger.Warn("PAIR_STOP_LOSS_TRIGGERED",
				slog.String("operation", o.ID),
				slog.Float64("spread", priceDiff),
				slog.Float64("stop_threshold", o.thresholdPriceDiff*defaultPairStopLossMultiplier),
			)
			o.strategyA.SetTarget(strategy.TargetPosition{Qty: 0.0, Price: 0.0, OrderType: order.ORDER_TYPE_MARKET, Reason: "PairStopLoss_FlatA"})
			o.strategyB.SetTarget(strategy.TargetPosition{Qty: 0.0, Price: 0.0, OrderType: order.ORDER_TYPE_MARKET, Reason: "PairStopLoss_FlatB"})
		} else if o.isEODForceExitTime(now) {
			o.logger.Warn("PAIR_EOD_EXIT_TRIGGERED",
				slog.String("operation", o.ID),
				slog.Time("tick_time", now),
			)
			o.strategyA.SetTarget(strategy.TargetPosition{Qty: 0.0, Price: 0.0, OrderType: order.ORDER_TYPE_MARKET, Reason: "PairEODExit_FlatA"})
			o.strategyB.SetTarget(strategy.TargetPosition{Qty: 0.0, Price: 0.0, OrderType: order.ORDER_TYPE_MARKET, Reason: "PairEODExit_FlatB"})
		}
	}

	// 5. 2つの SniperNest にTick処理を伝達し、個別のアクション結果をマージして返却
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

func (o *PairTradingOperation) ReconcilePositions(actualPositions []position.Position) {
	o.nestA.ReconcilePositions(actualPositions)
	o.nestB.ReconcilePositions(actualPositions)
}

func (o *PairTradingOperation) ForceExit() {
	o.nestA.ForceExit()
	o.nestB.ForceExit()
}

func (o *PairTradingOperation) OrderlyExit() {
	o.nestA.OrderlyExit()
	o.nestB.OrderlyExit()
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

func (o *PairTradingOperation) FailCancelingOrder(sniperID string, orderID string) {
	if o.nestA.HasSniper(sniperID) {
		o.nestA.FailCancelingOrder(sniperID, orderID)
	} else if o.nestB.HasSniper(sniperID) {
		o.nestB.FailCancelingOrder(sniperID, orderID)
	}
}

func (o *PairTradingOperation) DestroySendingOrder(sniperID string, ord *order.Order) {
	if o.nestA.HasSniper(sniperID) {
		o.nestA.DestroySendingOrder(sniperID, ord)
	} else if o.nestB.HasSniper(sniperID) {
		o.nestB.DestroySendingOrder(sniperID, ord)
	}
}

func (o *PairTradingOperation) HandleOrderRejection(sniperID string, ord *order.Order, err error) {
	if o.nestA.HasSniper(sniperID) {
		o.nestA.HandleOrderRejection(sniperID, ord, err)
	} else if o.nestB.HasSniper(sniperID) {
		o.nestB.HandleOrderRejection(sniperID, ord, err)
	}
	// 片方のエントリー注文が拒絶された場合は、もう一方の建玉や発注が残らないよう両方の目標を0へリセットする
	if ord != nil && ord.CashMargin == order.CASH_MARGIN_MARGIN_ENTRY {
		o.strategyA.SetTarget(strategy.TargetPosition{Qty: 0.0, Price: 0.0, OrderType: order.ORDER_TYPE_MARKET, Reason: "PairUnwind_RejectedA"})
		o.strategyB.SetTarget(strategy.TargetPosition{Qty: 0.0, Price: 0.0, OrderType: order.ORDER_TYPE_MARKET, Reason: "PairUnwind_RejectedB"})
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
