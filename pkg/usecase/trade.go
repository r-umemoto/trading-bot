// internal/usecase/trade_usecase.go
package usecase

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/report"
	"github.com/r-umemoto/trading-bot/pkg/domain/service"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

// TradeUseCase は価格更新イベントを受け取り、該当するスナイパーに伝達するユースケースです
type TradeUseCase struct {
	operations          []sniper.Operation
	gateway             market.MarketGateway
	lastZombieReconcile map[string]time.Time
	zombieMu            sync.Mutex
	reportRepo          report.Repository
}

func NewTradeUseCase(operations []sniper.Operation, gateway market.MarketGateway, reportRepo report.Repository) *TradeUseCase {
	return &TradeUseCase{
		operations:          operations,
		gateway:             gateway,
		lastZombieReconcile: make(map[string]time.Time),
		reportRepo:          reportRepo,
	}
}

// Start は市場データ受信を開始し、各作戦ごとのイベントループを起動します
func (u *TradeUseCase) Start(ctx context.Context, chs *market.MarketChannels) {
	for _, op := range u.operations {
		symbols := op.GetSymbolCodes()

		mergedTickCh := make(chan tick.Tick, 100)
		mergedOrderCh := make(chan order.Orders, 100)

		for _, sym := range symbols {
			tickCh := chs.Ticks[sym]
			orderCh := chs.Orders[sym]
			if tickCh != nil {
				go func(c <-chan tick.Tick) {
					for t := range c {
						select {
						case <-ctx.Done():
							return
						case mergedTickCh <- t:
						}
					}
				}(tickCh)
			}
			if orderCh != nil {
				go func(c <-chan order.Orders) {
					for o := range c {
						select {
						case <-ctx.Done():
							return
						case mergedOrderCh <- o:
						}
					}
				}(orderCh)
			}
		}

		go u.runOperationEventLoop(ctx, op, mergedTickCh, mergedOrderCh)
	}
}

// runOperationEventLoop は特定の作戦のイベントループを非同期に監視します
func (u *TradeUseCase) runOperationEventLoop(ctx context.Context, op sniper.Operation, tickCh <-chan tick.Tick, orderCh <-chan order.Orders) {
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-tickCh:
			// ドメイン集約にビジネスロジックの評価を委譲 (純粋関数)
			actions := op.HandleTick(t)
			for _, act := range actions {
				go u.fire(ctx, op, act.SniperID, act.Bullet)
			}
			// ゾンビ注文（キャンセル応答なしで膠着状態の注文）の自動監視と自己修復
			u.checkZombieOrders(ctx, op)
		case ords := <-orderCh:
			op.UpdateOrders(ords)
		}
	}
}

// checkZombieOrders はアクティブ注文に時間超過したキャンセル送信中注文がないか監視します
func (u *TradeUseCase) checkZombieOrders(ctx context.Context, op sniper.Operation) {
	now := time.Now()
	var hasZombie bool

	for _, ord := range op.GetActiveOrders() {
		if ord.Status == order.ORDER_STATUS_CANCEL_SENT && !ord.CancelSentAt.IsZero() {
			timeout := ord.GetCancelTimeout()
			if now.Sub(ord.CancelSentAt) > timeout {
				hasZombie = true
				slog.Warn("🚨 [ZOMBIE_ORDER_DETECTED] キャンセル送信タイムアウト超過を検知しました",
					slog.String("opID", op.GetID()),
					slog.String("orderID", ord.ID),
					slog.String("symbol", ord.Symbol),
					slog.String("reason", ord.Reason),
					slog.Duration("elapsed", now.Sub(ord.CancelSentAt)),
					slog.Duration("timeout", timeout),
				)
			}
		}
	}

	if hasZombie {
		u.zombieMu.Lock()
		lastReconcile := u.lastZombieReconcile[op.GetID()]
		if now.Sub(lastReconcile) > 5*time.Second {
			u.lastZombieReconcile[op.GetID()] = now
			u.zombieMu.Unlock()
			go u.reconcileZombieOrder(ctx, op)
		} else {
			u.zombieMu.Unlock()
		}
	}
}

// reconcileZombieOrder は最新の注文一覧を能動照会し、自己復旧を試みます
func (u *TradeUseCase) reconcileZombieOrder(ctx context.Context, op sniper.Operation) {
	slog.Info("🔍 [ZOMBIE_RECONCILIATION] 最新の注文状態を取引所APIに能動照会し、自己復旧を試みます...", slog.String("opID", op.GetID()))
	ords, err := u.gateway.GetOrders(ctx)
	if err != nil {
		slog.Error("❌ [ZOMBIE_RECONCILIATION] 注文状態の取得に失敗しました", slog.String("opID", op.GetID()), slog.Any("error", err))
		return
	}
	op.UpdateOrders(ords)
	slog.Info("✅ [ZOMBIE_RECONCILIATION] 自己復旧用の同期照会が完了しました", slog.String("opID", op.GetID()))
}

// fire は実際の発注・キャンセル処理を API ゲートウェイに対して非同期に実行します
func (u *TradeUseCase) fire(ctx context.Context, op sniper.Operation, sniperID string, b sniper.Bullet) {
	if b.HasCancel() {
		err := u.gateway.CancelOrder(ctx, b.CancelOrderID)
		if err != nil {
			slog.Error("❌ キャンセル注文の送信に失敗しました",
				slog.String("orderID", b.CancelOrderID),
				slog.Any("error", err),
			)
		}
	}

	if b.HasOrder() {
		updatedOrder, err := u.gateway.SendOrder(ctx, order.SendOrderInput{Order: b.Order, Request: *b.Request})
		if err != nil {
			slog.Warn("⚠️ [SendOrder_API_ERROR] 発注処理中にエラーまたはタイムアウトを検知しました。Orphan Position防止のため即時状態照合(Reconciliation)を行います...",
				slog.String("symbol", b.Order.Symbol),
				slog.String("localID", b.Order.ID),
				slog.Any("error", err),
			)

			// タイムアウトやネットワークエラーによる Orphan Position を防ぐため、即時 GetOrders で証券会社側の状態を能動取得
			reconcileCtx, reconcileCancel := context.WithTimeout(context.Background(), 5*time.Second)
			ords, recErr := u.gateway.GetOrders(reconcileCtx)
			reconcileCancel()

			if recErr != nil {
				slog.Error("❌ [SendOrder_RECONCILIATION_FAILED] 状態照合のための GetOrders に失敗しました。安全のため注文失敗として扱います",
					slog.String("symbol", b.Order.Symbol),
					slog.Any("error", recErr),
				)
				op.FailSendingOrder(sniperID, b.Order)
				return
			}

			// GetOrders の結果から、同一銘柄・同一売買区分・同一数量・同一価格の未登録な注文が存在するか確認
			var matchedOrder *order.Order
			for _, ext := range ords.Orders {
				if ext.Symbol == b.Order.Symbol &&
					ext.Action == b.Order.Action &&
					ext.OrderQty == b.Order.OrderQty &&
					ext.OrderPrice == b.Order.OrderPrice {

					// このスナイパーまたは他のスナイパーが既に追跡している注文IDは除外
					alreadyTracked := false
					for _, opOther := range u.operations {
						for _, actOrd := range opOther.GetActiveOrders() {
							if actOrd.ID == ext.ID {
								alreadyTracked = true
								break
							}
						}
						if alreadyTracked {
							break
						}
					}

					if !alreadyTracked {
						matchedOrder = &ext
						break
					}
				}
			}

			if matchedOrder != nil {
				slog.Info("🎯 [SendOrder_RECONCILED] タイムアウトした注文が証券会社側で受理されていることを確認しました！注文IDを更新して追跡します",
					slog.String("symbol", b.Order.Symbol),
					slog.String("localID", b.Order.ID),
					slog.String("serverID", matchedOrder.ID),
				)
				// 注文IDをサーバー発行のものに更新してActiveOrdersで生存させる
				op.UpdateOrderID(sniperID, b.Order, matchedOrder.ID)
				// 状態も同期
				op.UpdateOrders(ords)
				return
			}

			slog.Warn("🚫 [SendOrder_NOT_ACCEPTED] 状態照合の結果、証券会社側に該当する注文が見つかりませんでした。発注は実際に行われなかったと判断します",
				slog.String("symbol", b.Order.Symbol),
				slog.String("localID", b.Order.ID),
			)
			op.FailSendingOrder(sniperID, b.Order)
			return
		}
		op.UpdateOrderID(sniperID, b.Order, updatedOrder.ID)
	}
}

func (u *TradeUseCase) PrintPerformanceReport(enableCSV bool) {
	var targets []sniper.ReportableTarget
	for _, op := range u.operations {
		targets = append(targets, op.GetReportableTargets()...)
	}
	reportData := service.GeneratePerformanceReport(u, targets, u.gateway.DataPool())
	presenter := NewReportPresenter()
	presenter.PrintPerformanceReport(reportData)

	// 自動保存ロジックの追加
	mapAggregated := func(p *service.AggregatedPerformance) report.AggregatedPerformance {
		if p == nil {
			return report.AggregatedPerformance{}
		}
		winRate := 0.0
		if p.Trades > 0 {
			winRate = float64(p.Wins) / float64(p.Trades) * 100
		}
		return report.AggregatedPerformance{
			Name:          p.Name,
			Trades:        p.Trades,
			Wins:          p.Wins,
			Losses:        p.Losses,
			Draws:         p.Trades - p.Wins - p.Losses,
			WinRate:       winRate,
			RealizedPnL:   p.RealizedPnL,
			UnrealizedPnL: p.UnrealizedPnL,
			TotalPnL:      p.RealizedPnL + p.UnrealizedPnL,
		}
	}

	var symbols []report.AggregatedPerformance
	for _, p := range reportData.Symbols {
		symbols = append(symbols, mapAggregated(p))
	}
	var strats []report.AggregatedPerformance
	for _, p := range reportData.Strats {
		strats = append(strats, mapAggregated(p))
	}
	var combined []report.AggregatedPerformance
	for _, p := range reportData.Combined {
		combined = append(combined, mapAggregated(p))
	}

	// 日本時間 (JST) での日付文字列を取得
	jst, err := time.LoadLocation("Asia/Tokyo")
	var dateStr string
	if err == nil {
		dateStr = time.Now().In(jst).Format("2006-01-02")
	} else {
		dateStr = time.Now().Format("2006-01-02")
	}

	dailyReport := &report.DailyReport{
		Date:      dateStr,
		UpdatedAt: time.Now(),
		Total:     mapAggregated(reportData.Total),
		Symbols:   symbols,
		Strats:    strats,
		Combined:  combined,
	}

	if u.reportRepo != nil {
		if err := u.reportRepo.Save(context.Background(), dailyReport); err != nil {
			slog.Error("❌ 成績の自動保存に失敗しました", slog.Any("error", err))
		} else {
			slog.Info("💾 成績を自動保存しました", slog.String("date", dailyReport.Date))
		}
	}
}

func (u *TradeUseCase) GetPerformance(sniperID string) sniper.Performance {
	for _, op := range u.operations {
		if op.HasSniper(sniperID) {
			return op.GetPerformance(sniperID)
		}
	}
	return sniper.Performance{}
}

func (u *TradeUseCase) GetUnrealizedPnL(sniperID string, currentPrice float64) float64 {
	for _, op := range u.operations {
		if op.HasSniper(sniperID) {
			return op.GetUnrealizedPnL(sniperID, currentPrice)
		}
	}
	return 0
}
