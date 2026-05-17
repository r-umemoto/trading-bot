package usecase

import (
	"context"

	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

// UseCaseHandler はシステムライフサイクルユースケースとトレードユースケースを統合的に管理・委譲するファサード構造体です
type UseCaseHandler struct {
	system *SystemUseCase
	trade  *TradeUseCase
}

func NewUseCaseHandler(system *SystemUseCase, trade *TradeUseCase) *UseCaseHandler {
	return &UseCaseHandler{
		system: system,
		trade:  trade,
	}
}

// Initialize はシステム起動処理と発注ディスパッチャ起動をまとめて実行します
func (h *UseCaseHandler) Initialize(ctx context.Context) error {
	// 1. システム初期化（残存決済、銘柄登録）
	if err := h.system.Initialize(ctx); err != nil {
		return err
	}

	// 2. ディスパッチャの起動
	h.trade.StartDispatcher(ctx)
	return nil
}

// Shutdown はシステム終了時のポジション全決済と銘柄登録解除を行います
func (h *UseCaseHandler) Shutdown(ctx context.Context) error {
	return h.system.Shutdown(ctx)
}

// ExecuteTick は指定された銘柄の価格更新（Tick）を受け取り、同期的にスナイパー戦略を処理・評価します
func (h *UseCaseHandler) ExecuteTick(ctx context.Context, t tick.Tick) {
	h.trade.ExecuteTick(ctx, t)
}

// ExecuteExecutionReport は最新の注文レポートを受け取り、同期的にスナイパーと注文状態の同期を行います
func (h *UseCaseHandler) ExecuteExecutionReport(ctx context.Context, report order.Orders, symbol string) {
	h.trade.ExecuteExecutionReport(ctx, report, symbol)
}

// PrintReport は全スナイパーの成績を集計し、出力およびCSV保存を行います
func (h *UseCaseHandler) PrintReport(enableCSV bool) {
	h.trade.PrintPerformanceReport(enableCSV)
}

// GetSymbols は監視対象となっている全ての銘柄コード（重複排除済み）のリストを返します
func (h *UseCaseHandler) GetSymbols() []string {
	return h.system.GetSymbols()
}
