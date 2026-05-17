package usecase

import (
	"context"
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

// Start はシステム起動処理と取引処理のスレッド群を起動します
func (h *UseCaseHandler) Start(ctx context.Context) error {
	// 1. システム初期化（残存決済、銘柄登録）
	if err := h.system.Initialize(ctx); err != nil {
		return err
	}

	// 2. 市場接続ストリーミングの開始（リスン）
	ticks, orders, err := h.system.Listen(ctx)
	if err != nil {
		return err
	}

	// 3. 取引処理（ディスパッチャおよび各銘柄ワーカー）の起動
	h.trade.Start(ctx, ticks, orders)
	return nil
}

// Shutdown はシステム終了時のポジション全決済と銘柄登録解除を行います
func (h *UseCaseHandler) Shutdown(ctx context.Context) error {
	return h.system.Shutdown(ctx)
}

// PrintReport は全スナイパーの成績を集計し、出力およびCSV保存を行います
func (h *UseCaseHandler) PrintReport(enableCSV bool) {
	h.trade.PrintPerformanceReport(enableCSV)
}


