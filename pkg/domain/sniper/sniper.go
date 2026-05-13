package sniper

import (
	"fmt"
	"math"
	"sync"
	"time"

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
	ExecutionPolicy strategy.ExecutionPolicy // 👈 執行ポリシー（疑似約定判定）
	Orders          []*market.Order
	mu              sync.Mutex // 👈 状態をロックするための鍵
	isExiting       bool       // 👈 撤収作業中かどうかのフラグ
	AccountType     market.AccountType
	Exchange        market.ExchangeMarket
	MarginTradeType market.MarginTradeType

	processedExecutions map[string]bool // 🌟 処理済みの約定IDを記録
}

// NewSniper の引数と戻り値も修正
func NewSniper(detail market.Symbol, strategy Strategy, policy strategy.ExecutionPolicy, exchange market.ExchangeMarket) *Sniper {
	return &Sniper{
		Detail:          detail,
		Strategy:        strategy,
		ExecutionPolicy: policy,
		Orders:          make([]*market.Order, 0),
		positions:       []market.Position{}, // 初期状態は空
		AccountType:     market.ACCOUNT_SPECIAL,
		Exchange:        exchange,
		MarginTradeType: market.TRADE_TYPE_GENERAL_DAY,
		processedExecutions: make(map[string]bool),
	}
}

// 価格の更新がされたと時に実行される監視ロジック
func (s *Sniper) Tick(dataPool market.DataPool) (*market.Order, *market.OrderRequest, string) {
	// 処理中は他のゴルーチンが状態を触れないようにロック！
	s.mu.Lock()
	defer s.mu.Unlock() // 関数が終わったら必ずロック解除

	// --- 0. クリーンアップフェーズ ---
	s.cleanupZombiesLocked()

	// 0. 呼値を最新の価格に基づいて更新
	state := dataPool.GetState(s.Detail.Code)

	// すでにキルスイッチが作動（撤収中）なら、価格更新はすべて無視！
	if s.isExiting {
		return nil, nil, ""
	}

	// 1. 管理対象の注文を特定する（アクションに合わせた注文を抽出）
	var activeBuyOrder *market.Order
	var activeSellOrder *market.Order
	for _, o := range s.Orders {
		if o.IsCompleted() || o.Status == market.ORDER_STATUS_CANCEL_SENT {
			continue
		}
		if o.Action == market.ACTION_BUY {
			activeBuyOrder = o
		} else if o.Action == market.ACTION_SELL {
			activeSellOrder = o
		}
	}

	// 1.5 疑似約定 (Synthetic Fill) の判定 (両方の注文に対して実施)
	if s.ExecutionPolicy != nil {
		if activeBuyOrder != nil && !market.IsPendingID(activeBuyOrder.ID) {
			s.ExecutionPolicy.ApplySyntheticFill(activeBuyOrder, state.LatestTick)
		}
		if activeSellOrder != nil && !market.IsPendingID(activeSellOrder.ID) {
			s.ExecutionPolicy.ApplySyntheticFill(activeSellOrder, state.LatestTick)
		}
	}

	input := strategy.StrategyInput{
		Orders:        strategy.StrategyOrders(s.Orders),
		LatestTick:    state.LatestTick,
		BasePositions: s.positions,
	}

	// 3. 戦略の判断を仰ぐ
	signal := s.Strategy.Evaluate(input)

	// 現在の判断に関係する注文を特定
	var activeOrder *market.Order
	marketAction, _ := signal.Action.ToMarketAction()
	if marketAction == market.ACTION_BUY {
		activeOrder = activeBuyOrder
	} else if marketAction == market.ACTION_SELL {
		activeOrder = activeSellOrder
	}

	// --- 4. 同期（Reconciliation）フェーズ ---

	// すでに注文が出ている場合
	if activeOrder != nil {
		// IDがまだ確定していない（PENDING）場合は、次のアクションを起こさず待機
		if market.IsPendingID(activeOrder.ID) {
			marketAction, _ := signal.Action.ToMarketAction()
			if marketAction != "" && activeOrder.Action != marketAction {
				// 決済（反対売買）の場合は、PENDINGを無視して新規発注フローへ進ませる（デッドロック防止）
				fmt.Printf("⚠️ [%s] PENDING中の注文(%s)がありますが、反対売買(Action: %s)のため発注を優先します\n", s.Detail.Code, activeOrder.Action, marketAction)
				activeOrder = nil
			} else {
				return nil, nil, ""
			}
		}

		// すでにキャンセル送信済みの場合は、その確定を待つ
		if activeOrder.Status == market.ORDER_STATUS_CANCEL_SENT {
			// ここに来るということは、上記のループで skip されなかった場合（二重キャンセル防止など）
			return nil, nil, ""
		}

		// 疑似約定済みの場合は、基本的には維持するが、
		// 戦略が「次の注文（利確など）」を出したい場合は、この注文を「完了したもの」とみなして
		// 重複発注プロセスへ進ませる。
		if activeOrder.Status == market.ORDER_STATUS_FILL_EXPECTED {
			// ここでは何もせず、後続の「意図との比較」へ進む
		}

		// 現在の注文が戦略の意図（シグナル）と一致しているかチェック
		isStillDesired := false
		if signal.Action != brain.ACTION_HOLD {
			marketAction, _ := signal.Action.ToMarketAction()
			// 方向・数量が一致しているか
			if activeOrder.Action == marketAction && activeOrder.OrderQty == signal.Quantity {
				// 意図が成行（Price=0）で現在の注文も成行（OrderPrice=0）の場合
				if signal.Price == 0 && activeOrder.OrderPrice == 0 {
					isStillDesired = true
				} else if signal.Price > 0 && activeOrder.OrderPrice > 0 {
					// 指値の場合は、戦略が指定した価格と【完全に一致】しているかチェック
					// （※float64の微小な誤差を考慮して 0.0001 未満の差なら一致とみなす）
					if math.Abs(activeOrder.OrderPrice-signal.Price) < 0.0001 {
						isStillDesired = true
					}
				}
			}
		}

		if isStillDesired {
			// IFD情報のみの更新チェック
			if signal.HasIFD {
				marketIFDAction, err := signal.IFDAction.ToMarketAction()
				if err == nil {
					if activeOrder.HasIFD {
						if activeOrder.IFDPrice != signal.IFDPrice || activeOrder.IFDAction != marketIFDAction || activeOrder.IFDOrderType != signal.IFDOrderType {
							fmt.Printf("🔄 [%s] 待機中注文のIFD情報を更新します\n", s.Detail.Code)
							activeOrder.IFDPrice = signal.IFDPrice
							activeOrder.IFDAction = marketIFDAction
							activeOrder.IFDOrderType = signal.IFDOrderType
						}
					} else {
						// IFDが後から追加された
						fmt.Printf("🔄 [%s] 待機中注文にIFD情報を追加します\n", s.Detail.Code)
						activeOrder.HasIFD = true
						activeOrder.IFDPrice = signal.IFDPrice
						activeOrder.IFDAction = marketIFDAction
						activeOrder.IFDOrderType = signal.IFDOrderType
					}
				}
			} else if activeOrder.HasIFD && !signal.HasIFD {
				// IFDが取り消された
				fmt.Printf("🔄 [%s] 待機中注文のIFD情報を取り消します\n", s.Detail.Code)
				activeOrder.HasIFD = false
			}
		}

		// 意図と異なる、または HOLD になった場合はキャンセルを要求
		if !isStillDesired {
			// 【重要】疑似約定済みの場合は、キャンセルを禁止して約定確定を待つ
			// これにより、価格の微細な変化による「キャンセル・再発注スパムループ」を防止する
			if activeOrder.Status == market.ORDER_STATUS_FILL_EXPECTED {
				return nil, nil, ""
			}

			fmt.Printf("🔄 [%s] 意図と異なる注文(%s: %s@%.1f)をキャンセル要求します (Signal: %s@%.1f, Qty: %.1f)\n", 
				s.Detail.Code, activeOrder.ID, activeOrder.Action, activeOrder.OrderPrice, signal.Action, signal.Price, signal.Quantity)
			activeOrder.Status = market.ORDER_STATUS_CANCEL_SENT
			activeOrder.CancelSentAt = time.Now()
			return nil, nil, activeOrder.ID
		}

		// 一致している場合は維持
		return nil, nil, ""
	}

	if signal.Action == brain.ACTION_HOLD {
		return nil, nil, ""
	}

	marketAction, err := signal.Action.ToMarketAction()
	if err != nil {
		return nil, nil, ""
	}

	// --- 5. 新規発注フェーズ ---
	// 未完了注文がない場合（または既存の注文がキャンセル送信中の場合）に、新規発注を検討する
	// ただし、同じアクション（BUY/BUY, SELL/SELL）の注文が一つでも残っている場合は、
	// 二重発注を避けるためにその完了（または確定）を絶対に待つ。
	for _, o := range s.Orders {
		if !o.IsCompleted() {
			if o.Action == marketAction {
				// すでに同じ方向の注文が（PENDING/WAITING/CANCEL_SENT問わず）存在する場合は、
				// 重複発注を避けるため、次のアクションを絶対に起こさない。
				if o.Status == market.ORDER_STATUS_CANCEL_SENT {
					// キャンセル送信から一定時間（例：30秒）経過しても応答がない場合はゾンビとみなし、
					// ブロックを解除して新規発注フローへの進行を許可する。
					if !o.CancelSentAt.IsZero() && time.Since(o.CancelSentAt) > 30*time.Second {
						fmt.Printf("⚠️ [%s] キャンセル送信から30秒以上経過しても応答がありません(ID:%s)。ゾンビとみなしてブロックを解除します。\n", s.Detail.Code, o.ID)
						continue
					}
					// キャンセルがAPI側で確定するのを待つ
					return nil, nil, ""
				}
				return nil, nil, ""
			}
		}
	}

	orderType := market.ORDER_TYPE_MARKET
	if signal.Price > 0 {
		orderType = market.ORDER_TYPE_LIMIT
	} else if signal.OrderType != 0 {
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

	var marketIFDAction market.Action
	if signal.HasIFD {
		marketIFDAction, _ = signal.IFDAction.ToMarketAction()
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
		HasIFD:             signal.HasIFD,
		IFDAction:          marketIFDAction,
		IFDPrice:           signal.IFDPrice,
		IFDOrderType:       signal.IFDOrderType,
	}

	// 仮IDで管理リストに追加
	ptr := market.NewOrderPtr(market.GeneratePendingID(), req.Symbol, req.Action, req.Price, req.Qty)
	ptr.HasIFD = req.HasIFD
	ptr.IFDAction = req.IFDAction
	ptr.IFDPrice = req.IFDPrice
	ptr.IFDOrderType = req.IFDOrderType
	s.Orders = append(s.Orders, ptr)

	return ptr, req, ""
}

// ConfirmOrder は、ユースケースが発注を完了した後に呼ばれ、仮注文のIDを正式なAPIのIDで更新します
func (s *Sniper) ConfirmOrder(order *market.Order, realID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	order.ID = realID
}

func (s *Sniper) cleanupZombiesLocked() {
	for i := 0; i < len(s.Orders); i++ {
		o := s.Orders[i]
		if o.IsCompleted() {
			continue
		}

		// 1. PENDING (仮ID) のタイムアウト (30秒)
		// 発注要求から30秒経ってもIDが確定しない場合は、通信失敗やAPIの受付拒否とみなす
		if market.IsPendingID(o.ID) {
			if time.Since(o.CreatedAt) > 30*time.Second {
				fmt.Printf("⚠️ [%s] PENDING注文がタイムアウト(30s)したため削除します: %s\n", s.Detail.Code, o.ID)
				s.Orders = append(s.Orders[:i], s.Orders[i+1:]...)
				i--
				continue
			}
		}

		// 2. FILL_EXPECTED (疑似約定) のタイムアウト (20秒)
		// 本物の約定通知が来ない場合は、板の動きを見誤ったか貫通しなかったとみなして元に戻す
		if o.Status == market.ORDER_STATUS_FILL_EXPECTED {
			if !o.Synthetic.ExpectedAt.IsZero() && time.Since(o.Synthetic.ExpectedAt) > 20*time.Second {
				fmt.Printf("⚠️ [%s] 疑似約定から20秒経過しても通知がないため、待機状態に戻します: %s\n", s.Detail.Code, o.ID)
				o.Status = market.ORDER_STATUS_IN_PROGRESS
			}
		}
	}
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

// SyncOrders はインフラ層から取得した最新の注文一覧と同期し、内部状態を「事実」に合わせます
func (s *Sniper) SyncOrders(externalOrders []market.Order) (*market.Order, *market.OrderRequest, string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var newExecQty float64
	var triggeredIFDParent *market.Order
	var newExecs []market.Execution
	
	// 新しい管理リストを作成（事実ベースで再構築）
	var reconciledOrders []*market.Order

	// 1. IDが未確定（PENDING）または未完了の注文をまず保持する
	// APIの一覧に反映されるまでのタイムラグによる注文消失（および再発注スパム）を防ぐため。
	for _, o := range s.Orders {
		if market.IsPendingID(o.ID) {
			// 仮ID（PENDING）はAPI未達の可能性があるため、30秒間は無条件で保持する
			if time.Since(o.CreatedAt) < 30*time.Second {
				reconciledOrders = append(reconciledOrders, o)
			} else {
				fmt.Printf("🗑️ [%s] APIに30秒以上現れない仮ID(%s)をOrdersから削除します\n", s.Detail.Code, o.ID)
			}
		} else if !o.IsCompleted() {
			// 既にIDが確定している未完了注文は、APIの瞬断に備えて一旦リストに残す
			// （確定済み注文は30秒制限の対象外。指値でずっと待機している場合があるため）
			reconciledOrders = append(reconciledOrders, o)
		}
	}

	// 2. APIから取得した注文をすべて反映・採用する
	for _, ext := range externalOrders {
		if ext.Symbol != s.Detail.Code {
			continue
		}

		// 内部で管理している注文を探す
		var matchedInternal *market.Order
		for _, o := range s.Orders {
			if o.ID == ext.ID {
				matchedInternal = o
				break
			}
		}

		if matchedInternal == nil {
			// 【重要】自分が出した注文ではない（他の戦略の注文など）可能性があるため、一切関知しない。
			// 起動時の残存建玉は PositionCleaner が掃除するため、ここでは「自分がこのセッションで出した注文」のみを管理する。
			continue
		}

		// 状態の同期
		matchedInternal.Status = ext.Status
		matchedInternal.CumQty = ext.CumQty

		// 約定の反映
		for _, exec := range ext.Executions {
			if !s.processedExecutions[exec.ID] {
				s.applyExecution(exec, matchedInternal.Action)
				newExecQty += exec.Qty
				triggeredIFDParent = matchedInternal
				newExecs = append(newExecs, exec)
			}
		}

		// 完了していない注文を管理リストに残す
		isDataComplete := matchedInternal.FilledQty() >= matchedInternal.CumQty
		if !matchedInternal.IsCompleted() || !isDataComplete {
			exists := false
			for _, ro := range reconciledOrders {
				if ro.ID == matchedInternal.ID {
					exists = true
					break
				}
			}
			if !exists {
				reconciledOrders = append(reconciledOrders, matchedInternal)
			}
		}
	}

	// リストを更新（APIにない確定済みIDの注文はここで消える）
	s.Orders = reconciledOrders

	// 3. IFDの発火処理
	var ifdPendingOrder *market.Order
	var ifdRequest *market.OrderRequest
	if newExecQty > 0 && triggeredIFDParent != nil && triggeredIFDParent.HasIFD {
		fmt.Printf("⚡ [%s] IFD発火: %.2f株の約定に伴い、決済注文を発注します\n", s.Detail.Code, newExecQty)

		var closePositions []market.ClosePosition
		if triggeredIFDParent.IFDAction == market.ACTION_SELL {
			for _, exec := range newExecs {
				closePositions = append(closePositions, market.ClosePosition{
					HoldID: exec.ID,
					Qty:    exec.Qty,
				})
			}
		}

		closeOrder := market.CLOSE_POSITION_ORDER_NONE
		if triggeredIFDParent.IFDAction == market.ACTION_SELL && len(closePositions) == 0 {
			closeOrder = market.CLOSE_POSITION_ASC_DAY_DEC_PL
		}

		ifdRequest = &market.OrderRequest{
			Symbol:             s.Detail.Code,
			Exchange:           s.Exchange,
			SecurityType:       market.SECURITY_TYPE_STOCK,
			Action:             triggeredIFDParent.IFDAction,
			MarginTradeType:    market.TRADE_TYPE_GENERAL_DAY,
			AccountType:        market.ACCOUNT_SPECIAL,
			OrderType:          triggeredIFDParent.IFDOrderType,
			ClosePositionOrder: closeOrder,
			ClosePositions:     closePositions,
			Qty:                newExecQty,
			Price:              triggeredIFDParent.IFDPrice,
			HasIFD:             false,
		}

		ifdPendingOrder = market.NewOrderPtr(market.GeneratePendingID(), ifdRequest.Symbol, ifdRequest.Action, ifdRequest.Price, ifdRequest.Qty)
		s.Orders = append(s.Orders, ifdPendingOrder)
	}

	return ifdPendingOrder, ifdRequest, ""
}

func (s *Sniper) applyExecution(exec market.Execution, action market.Action) {
	// 🌟 重複チェック（冪等性の担保）
	if s.processedExecutions[exec.ID] {
		return
	}
	s.processedExecutions[exec.ID] = true

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
