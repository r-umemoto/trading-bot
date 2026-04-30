package sniper

import (
	"fmt"
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
	Symbol          string
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
func NewSniper(symbol string, strategy Strategy, exchange market.ExchangeMarket) *Sniper {
	return &Sniper{
		Symbol:          symbol,
		Strategy:        strategy,
		Orders:          make([]*market.Order, 0),
		positions:       []market.Position{}, // 初期状態は空
		AccountType:     market.ACCOUNT_SPECIAL,
		Exchange:        exchange,
		MarginTradeType: market.TRADE_TYPE_GENERAL_DAY,
	}
}

// 価格の更新がされたと時に実行される監視ロジック
func (s *Sniper) Tick(dataPool market.DataPool) (*market.Order, *market.OrderRequest) {
	// 処理中は他のゴルーチンが状態を触れないようにロック！
	s.mu.Lock()
	defer s.mu.Unlock() // 関数が終わったら必ずロック解除

	// すでにキルスイッチが作動（撤収中）なら、価格更新はすべて無視！
	if s.isExiting {
		return nil, nil
	}

	// 1. 現在の建玉から必要なパラメータを計算（抽出）する
	var holdQty float64
	var totalExposure float64
	for _, p := range s.positions {
		holdQty += p.LeavesQty
		totalExposure += p.Price * float64(p.LeavesQty) // 取得単価 × 数量
	}

	// 発注済みで、まだ約定していない注文の「未約定数量」を合計する
	var pendingSellQty float64
	var pendingBuyQty float64
	for _, order := range s.Orders { // スナイパーが管理している現在の注文リスト
		unexecutedQty := order.OrderQty - order.FilledQty()
		if unexecutedQty > 0 {
			switch order.Action {
			case market.ACTION_SELL:
				pendingSellQty += unexecutedQty
			case market.ACTION_BUY:
				pendingBuyQty += unexecutedQty
			}
		}
	}

	// 戦略に渡す「自由に動かせる株数」
	freeQty := holdQty - pendingSellQty
	if freeQty < 0 {
		freeQty = 0 // 念のためのマイナス防止
	}

	averagePrice := 0.0
	if freeQty > 0 {
		averagePrice = totalExposure / float64(freeQty)
	}

	state := dataPool.GetState(s.Symbol)
	input := strategy.StrategyInput{
		Symbol:        s.Symbol,
		HoldQty:       freeQty,
		AveragePrice:  averagePrice,
		TotalExposure: totalExposure,
		LatestTick:    state.LatestTick,
	}

	// 1. 頭脳に価格を渡して判断を仰ぐ
	signal := s.Strategy.Evaluate(input)

	if signal.Action == brain.ACTION_HOLD {
		return nil, nil // 何もしない
	}

	// スナイパー（執行役）側で重複発注をブロックする
	// ※ 将来的に高度な注文管理を行う場合は、専用の「OrderManager」に委譲する想定
	if signal.Action == brain.ACTION_BUY && pendingBuyQty > 0 {
		return nil, nil
	}

	marketAction, err := signal.Action.ToMarketAction()
	if err != nil {
		fmt.Println("トラップできていないエラーがあります")
		return nil, nil
	}

	orderType := market.ORDER_TYPE_MARKET
	if signal.OrderType != 0 {
		orderType = signal.OrderType
	}

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
		Symbol:             s.Symbol,
		Exchange:           s.Exchange,
		SecurityType:       market.SECURITY_TYPE_STOCK,
		Action:             marketAction,
		MarginTradeType:    market.TRADE_TYPE_GENERAL_DAY,
		AccountType:        market.ACCOUNT_SPECIAL,
		OrderType:          orderType,
		ClosePositionOrder: closeOrder,     // ClosePositionsがない場合のみ指定
		ClosePositions:     closePositions, // 🌟 指定返済
		Qty:                signal.Quantity,
		Price:              signal.Price,
	}

	// 🌟 IDが空（または仮）の本物の注文データを作ってOrdersに混ぜておく
	pendingOrder := market.NewOrder("PENDING", req.Symbol, req.Action, req.Price, req.Qty)
	ptr := &pendingOrder
	s.Orders = append(s.Orders, ptr)

	return ptr, req
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

	fmt.Printf("🚨 [%s] 撤収フラグON。これ以降の価格更新は無視し、強制決済プロセスを開始します。\n", s.Symbol)

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

// OnExecution は、証券会社から約定通知を受信した際に呼び出されます。担当する注文であればtrueを返します。
func (s *Sniper) OnExecution(report market.ExecutionReport) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. 対象の注文状態を更新する
	var matchedOrder *market.Order
	var matchedOrderIndex = -1
	for i, order := range s.Orders {
		if order.ID == report.OrderID {
			matchedOrder = order
			matchedOrderIndex = i
			break
		}
	}

	if matchedOrder == nil {
		// 自身の注文ではないためスキップ
		return false
	}

	// 注文エンティティに約定を追加
	matchedOrder.AddExecution(market.Execution{
		ID:    report.ExecutionID,
		Price: report.Price,
		Qty:   report.Qty,
	})

	// もし全約定していたら、Activeリストから消す（履歴用リストに移す等）
	if matchedOrder.IsCompleted() {
		if matchedOrderIndex != -1 {
			s.Orders = append(s.Orders[:matchedOrderIndex], s.Orders[matchedOrderIndex+1:]...)
		}
	}

	// 2. 実際の約定結果に基づいて、建玉（Positions）を更新する
	switch report.Action {
	case market.ACTION_BUY:
		s.positions = append(s.positions, market.Position{
			ExecutionID: report.ExecutionID,
			Symbol:      report.Symbol,
			LeavesQty:   report.Qty,
			Price:       report.Price,
		})
		fmt.Printf("✅ [%s] 買付約定を反映: 単価%.2f 数量%f\n", s.Symbol, report.Price, report.Qty)
	case market.ACTION_SELL:
		s.reducePositions(report.Qty, report.Price)
		fmt.Printf("✅ [%s] 売付約定を反映: 数量%f\n", s.Symbol, report.Qty)
	}

	return true
}
