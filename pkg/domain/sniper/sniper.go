package sniper

import (
	"fmt"
	"math"
	"sync"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/brain"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
)

type Strategy interface {
	Name() string
	Evaluate(input strategy.StrategyInput) brain.Signal
}

// ★ スナイパー内で定義する「オプショナルな機能」の規格
type KillSwitchable interface {
	Activate() brain.Signal
}

type Performance struct {
	Trades        int
	Wins          int
	Losses        int
	RealizedPnL   float64
	UnrealizedPnL float64
}

// Sniper は戦略とAPIクライアントを持ち、執行を担います
type Sniper struct {
	Detail          market.Symbol // 🌟 銘柄詳細（Symbol, PriceRangeGroup等を含む）
	positions       []market.Position
	Performance     Performance // 🌟 追加
	Strategy        Strategy
	State           strategy.StrategyState // 👈 銘柄ごとの戦略ステート
	Orders          []*market.Order
	mu              sync.Mutex // 👈 状態をロックするための鍵
	isExiting       bool       // 👈 撤収作業中かどうかのフラグ
	AccountType     market.AccountType
	Exchange        market.ExchangeMarket
	MarginTradeType market.MarginTradeType
}

// NewSniper の引数と戻り値も修正
func NewSniper(detail market.Symbol, strategy Strategy, exchange market.ExchangeMarket) *Sniper {
	return &Sniper{
		Detail:          detail,
		Strategy:        strategy,
		Orders:          make([]*market.Order, 0),
		positions:       []market.Position{}, // 初期状態は空
		AccountType:     market.ACCOUNT_SPECIAL,
		Exchange:        exchange,
		MarginTradeType: market.TRADE_TYPE_GENERAL_DAY,
	}
}

// 価格の更新がされたと時に実行される監視ロジック
func (s *Sniper) Tick(dataPool market.DataPool) (*market.Order, *market.OrderRequest, string) {
	// 処理中は他のゴルーチンが状態を触れないようにロック！
	s.mu.Lock()
	defer s.mu.Unlock() // 関数が終わったら必ずロック解除

	// 0. 呼値を最新の価格に基づいて更新
	state := dataPool.GetState(s.Detail.Code)
	tickSize := 1.0 // デフォルト
	if state.LatestTick.Price > 0 {
		tickSize = s.Detail.CalcTickSize(state.LatestTick.Price)
	}

	// すでにキルスイッチが作動（撤収中）なら、価格更新はすべて無視！
	if s.isExiting {
		return nil, nil, ""
	}

	// 1. 未完了の注文（!IsCompleted）を抽出する
	var activeOrder *market.Order
	for _, o := range s.Orders {
		if !o.IsCompleted() {
			// 原則として1銘柄1注文なので、最初に見つかったものを対象とする
			activeOrder = o
			break
		}
	}

	// 2. パラメータ計算
	var holdQty float64
	var totalExposure float64
	for _, p := range s.positions {
		holdQty += p.LeavesQty
		totalExposure += p.Price * float64(p.LeavesQty)
	}

	averagePrice := 0.0
	if holdQty > 0 {
		averagePrice = totalExposure / holdQty
	}

	input := strategy.StrategyInput{
		Symbol:        s.Detail.Code,
		HoldQty:       holdQty,
		AveragePrice:  averagePrice,
		TotalExposure: totalExposure,
		LatestTick:    state.LatestTick,
	}

	// 3. 戦略の判断を仰ぐ
	signal := s.Strategy.Evaluate(input)

	// --- 4. 同期（Reconciliation）フェーズ ---

	// すでに注文が出ている場合
	if activeOrder != nil {
		// IDがまだ確定していない（PENDING）場合は、次のアクションを起こさず待機
		if market.IsPendingID(activeOrder.ID) {
			return nil, nil, ""
		}

		// すでにキャンセル送信済みの場合は、その確定を待つ
		if activeOrder.Status == market.ORDER_STATUS_CANCEL_SENT {
			return nil, nil, ""
		}

		// 現在の注文が戦略の意図（シグナル）と一致しているかチェック
		isStillDesired := false
		if signal.Action != brain.ACTION_HOLD {
			marketAction, _ := signal.Action.ToMarketAction()
			// 方向・数量が一致しており、かつ価格差が許容範囲（3ティック）以内かチェック
			if activeOrder.Action == marketAction &&
				activeOrder.OrderQty == signal.Quantity &&
				math.Abs(activeOrder.OrderPrice-signal.Price) <= tickSize*3 {
				isStillDesired = true
			}
		}

		// 意図と異なる、または HOLD になった場合はキャンセルを要求
		if !isStillDesired {
			activeOrder.Status = market.ORDER_STATUS_CANCEL_SENT
			return nil, nil, activeOrder.ID
		}

		// 一致している場合は維持
		return nil, nil, ""
	}

	// --- 5. 新規発注フェーズ ---
	// 未完了注文がない場合のみ、新規発注を検討する

	if signal.Action == brain.ACTION_HOLD {
		return nil, nil, ""
	}

	marketAction, err := signal.Action.ToMarketAction()
	if err != nil {
		return nil, nil, ""
	}

	orderType := market.ORDER_TYPE_MARKET
	if signal.OrderType != 0 {
		orderType = signal.OrderType
	}

	// 決済注文（売り）の場合のポジション指定ロジック
	var closePositions []market.ClosePosition
	if marketAction == market.ACTION_SELL {
		var remainingSellQty = signal.Quantity
		for _, p := range s.positions {
			if remainingSellQty <= 0 {
				break
			}
			closeQty := p.LeavesQty
			if closeQty > remainingSellQty {
				closeQty = remainingSellQty
			}
			closePositions = append(closePositions, market.ClosePosition{
				HoldID: p.ExecutionID,
				Qty:    closeQty,
			})
			remainingSellQty -= closeQty
		}
	}

	closeOrder := market.CLOSE_POSITION_ORDER_NONE
	if marketAction == market.ACTION_SELL && len(closePositions) == 0 {
		closeOrder = market.CLOSE_POSITION_ASC_DAY_DEC_PL
	}

	req := &market.OrderRequest{
		Symbol:             s.Detail.Code,
		Exchange:           s.Exchange,
		SecurityType:       market.SECURITY_TYPE_STOCK,
		Action:             marketAction,
		MarginTradeType:    market.TRADE_TYPE_GENERAL_DAY,
		AccountType:        market.ACCOUNT_SPECIAL,
		OrderType:          orderType,
		ClosePositionOrder: closeOrder,
		ClosePositions:     closePositions,
		Qty:                signal.Quantity,
		Price:              signal.Price,
	}

	// 仮IDで管理リストに追加
	pendingOrder := market.NewOrder(market.GeneratePendingID(), req.Symbol, req.Action, req.Price, req.Qty)
	ptr := &pendingOrder
	s.Orders = append(s.Orders, ptr)

	return ptr, req, ""
}

// ConfirmOrder は、ユースケースが発注を完了した後に呼ばれ、仮注文のIDを正式なAPIのIDで更新します
func (s *Sniper) ConfirmOrder(order *market.Order, realID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	order.ID = realID
}

// FailSendingOrder は発注失敗時に呼ばれ、Ordersリストから仮注文をクリアします
func (s *Sniper) FailSendingOrder(order *market.Order) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, o := range s.Orders {
		if o == order {
			// 該当するポインタをリストから削除
			s.Orders = append(s.Orders[:i], s.Orders[i+1:]...)
			break
		}
	}
}

// ForceExit はキルスイッチ作動時に呼ばれ、自身の未約定注文のキャンセルと成行決済を行います
func (s *Sniper) ForceExit() {
	s.mu.Lock()
	s.isExiting = true // 撤収フラグを立てる！
	s.mu.Unlock()      // フラグを立てたら、通信で詰まらないように一旦ロック解除

	fmt.Printf("🚨 [%s] 撤収フラグON。これ以降の価格更新は無視し、強制決済プロセスを開始します。\n", s.Detail.Code)

	// キルスイッチ機能を備えている戦略なら、発動させる
	if ks, ok := s.Strategy.(KillSwitchable); ok {
		_ = ks.Activate()
	}
}

// reducePositions は、指定された数量分だけ古い建玉から順に削減し、損益を計算します
func (s *Sniper) reducePositions(sellQty float64, sellPrice float64) {
	remainingToSell := sellQty
	var newPositions []market.Position

	for _, p := range s.positions {
		if remainingToSell <= 0 {
			// 売却分を消化しきったら、残りの建玉はそのまま保持リストへ
			newPositions = append(newPositions, p)
			continue
		}

		closeQty := p.LeavesQty
		if closeQty > remainingToSell {
			closeQty = remainingToSell
		}

		// 🌟 損益計算
		tradePnL := (sellPrice - p.Price) * closeQty
		s.Performance.RealizedPnL += tradePnL
		s.Performance.Trades++
		if tradePnL > 0 {
			s.Performance.Wins++
		} else if tradePnL < 0 {
			s.Performance.Losses++
		}

		if p.LeavesQty <= remainingToSell {
			// この建玉ロットを全量売却するケース
			remainingToSell -= p.LeavesQty
			// 全量売却なので newPositions には追加しない（消滅）
		} else {
			// この建玉ロットの一部だけを売却するケース
			p.LeavesQty -= remainingToSell
			remainingToSell = 0
			newPositions = append(newPositions, p)
		}
	}

	// 更新された建玉リストで上書き
	s.positions = newPositions
}

// CalcUnrealizedPnL は、現在の価格を基にスナイパーが保有する建玉の含み損益を計算します
func (s *Sniper) CalcUnrealizedPnL(currentPrice float64) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	var unrealized float64
	for _, p := range s.positions {
		unrealized += (currentPrice - p.Price) * p.LeavesQty
	}
	return unrealized
}

// SyncOrders はインフラ層から取得した最新の注文一覧と同期します
func (s *Sniper) SyncOrders(externalOrders []market.Order) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, ext := range externalOrders {
		if ext.Symbol != s.Detail.Code {
			continue
		}

		// 内部で管理している注文を探す
		var matchedInternal *market.Order
		for _, internal := range s.Orders {
			if internal.ID == ext.ID {
				matchedInternal = internal
				break
			}
		}

		if matchedInternal == nil {
			// まだIDが紐付いていない（Confirm前）の注文や、他で出された注文などは無視
			continue
		}

		// 1. 状態の同期
		matchedInternal.Status = ext.Status

		// 2. 新しい約定の反映
		for _, exec := range ext.Executions {
			if !matchedInternal.HasExecution(exec.ID) {
				// 新しい約定を発見
				matchedInternal.AddExecution(exec)
				s.applyExecution(exec, matchedInternal.Action)
			}
		}
	}

	// 3. 完了した注文をリストから除去
	var activeOrders []*market.Order
	for _, o := range s.Orders {
		if !o.IsCompleted() {
			activeOrders = append(activeOrders, o)
		}
	}
	s.Orders = activeOrders
}

func (s *Sniper) applyExecution(exec market.Execution, action market.Action) {
	switch action {
	case market.ACTION_BUY:
		s.positions = append(s.positions, market.Position{
			ExecutionID: exec.ID,
			Symbol:      s.Detail.Code,
			LeavesQty:   exec.Qty,
			Price:       exec.Price,
		})
		fmt.Printf("✅ [%s] 買付約定を反映: 単価%.2f 数量%f\n", s.Detail.Code, exec.Price, exec.Qty)
	case market.ACTION_SELL:
		s.reducePositions(exec.Qty, exec.Price)
		fmt.Printf("✅ [%s] 売付約定を反映: 数量%f\n", s.Detail.Code, exec.Qty)
	}
}

// OnExecution は廃止されました (SyncOrders に統合)
