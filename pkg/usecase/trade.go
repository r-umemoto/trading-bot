// internal/usecase/trade_usecase.go
package usecase

import (
	"context"
	"fmt"
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
			fmt.Printf("キャンセル失敗 (ID: %s): %v\n", b.CancelOrderID, err)
		}
	}

	if b.HasOrder() {
		updatedOrder, err := u.gateway.SendOrder(ctx, order.SendOrderInput{Order: b.Order, Request: *b.Request})
		if err != nil {
			fmt.Printf("発注失敗 (Symbol: %s): %v\n", op.GetSymbolCode(), err)
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
	presenter.PrintPerformanceReport(reportData, enableCSV)

	// 自動保存ロジックの追加
	var details []report.StrategyDetail
	for _, p := range reportData.Combined {
		details = append(details, report.StrategyDetail{
			Name:          p.Name,
			Trades:        p.Trades,
			Wins:          p.Wins,
			Losses:        p.Losses,
			RealizedPnL:   p.RealizedPnL,
			UnrealizedPnL: p.UnrealizedPnL,
		})
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
		Date:          dateStr,
		UpdatedAt:     time.Now(),
		Trades:        reportData.Total.Trades,
		Wins:          reportData.Total.Wins,
		Losses:        reportData.Total.Losses,
		WinRate:       0.0,
		RealizedPnL:   reportData.Total.RealizedPnL,
		UnrealizedPnL: reportData.Total.UnrealizedPnL,
		TotalPnL:      reportData.Total.RealizedPnL + reportData.Total.UnrealizedPnL,
		Details:       details,
	}
	if dailyReport.Trades > 0 {
		dailyReport.WinRate = float64(dailyReport.Wins) / float64(dailyReport.Trades) * 100
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
