package usecase

import (
	"context"
	"fmt"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/service"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
)

// SystemUseCase はシステムの起動時・終了時のライフサイクル処理を行うユースケースです
type SystemUseCase struct {
	snipers []*sniper.Sniper
	cleaner *service.PositionCleaner
	gateway market.MarketGateway
}

func NewSystemUseCase(snipers []*sniper.Sniper, gateway market.MarketGateway) *SystemUseCase {
	return &SystemUseCase{
		snipers: snipers,
		cleaner: service.NewPositionCleaner(snipers, gateway),
		gateway: gateway,
	}
}

// Initialize はシステム起動時の初期クリーンアップと銘柄登録を行います
func (s *SystemUseCase) Initialize(ctx context.Context) error {
	// 1. 起動時のクリーンアップ（残存注文・建玉の強制決済）
	if err := s.cleaner.CleanupOnStartup(ctx); err != nil {
		return err
	}

	// 2. 監視銘柄の登録
	fmt.Println("📡 監視銘柄の登録を開始します...")
	var reqs []market.ResisterSymbolRequest
	seen := make(map[string]bool)

	for _, sn := range s.snipers {
		key := fmt.Sprintf("%s:%d", sn.Detail.Code, sn.Exchange)
		if seen[key] {
			continue
		}
		reqs = append(reqs, market.ResisterSymbolRequest{
			Symbol:   sn.Detail.Code,
			Exchange: sn.Exchange,
		})
		seen[key] = true
	}

	if err := s.gateway.RegisterSymbols(ctx, reqs); err != nil {
		return fmt.Errorf("監視銘柄の登録に失敗: %w", err)
	}

	return nil
}

// Listen は市場ゲートウェイのストリーミングを開始します
func (s *SystemUseCase) Listen(ctx context.Context, handler market.MarketStreamHandler) error {
	return s.gateway.Listen(ctx, handler)
}

// Shutdown はシステム終了時のポジション全決済と銘柄の全解除を行います
func (s *SystemUseCase) Shutdown(ctx context.Context) error {
	// 1. 撤収・強制終了＆全ポジションのクローズ
	if err := s.cleaner.CleanAllPositions(ctx); err != nil {
		fmt.Printf("⚠️ ポジションクローズ失敗: %v\n", err)
	}

	// 2. 監視銘柄の全解除を保証
	fmt.Println("\n🧹 監視銘柄の登録を解除中...")
	if err := s.gateway.UnregisterSymbolAll(ctx); err != nil {
		return fmt.Errorf("銘柄登録解除に失敗: %w", err)
	}

	return nil
}


