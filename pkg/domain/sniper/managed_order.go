package sniper

import (
	"github.com/r-umemoto/trading-bot/pkg/domain/order"
)

// ManagedStatus は論理注文（ManagedOrder）のライフサイクル状態を表します
type ManagedStatus int

const (
	StatusEntryPreparing ManagedStatus = iota // エントリー準備中（未発注）
	StatusEntryActive                         // エントリー発注済み（取引所で稼働中）
	StatusExitPreparing                       // エントリー約定・決済注文発射準備（または発射直後）
	StatusExitActive                          // 決済注文発注済み（取引所で稼働中）
	StatusCompleted                           // すべて完了（全約定）
	StatusCanceled                            // キャンセル終了
)

// ManagedOrder は一つのトレード（エントリーから決済まで）を管理する論理的な単位です。
// シンプルな注文の場合は Exit が nil になります。IFDの場合は両方がセットされます。
type ManagedOrder struct {
	ID    string
	Type  order.OrderType // 論理的な種類 (通常指値、IFD等)
	Entry *order.Order    // エントリー注文（買いなど）
	Exit  *order.Order    // 決済注文（売りなど。IFDの場合に使用）

	Status ManagedStatus
}

// NewManagedOrder は新しい論理注文を生成します。
func NewManagedOrder(id string, entry *order.Order, exit *order.Order) *ManagedOrder {
	return &ManagedOrder{
		ID:    id,
		Entry: entry,
		Exit:  exit,
		Status: StatusEntryPreparing,
	}
}

// IsCompleted はこの論理注文が完全に終了したかどうかを判定します。
func (m *ManagedOrder) IsCompleted() bool {
	return m.Status == StatusCompleted || m.Status == StatusCanceled
}

// CurrentOrder は現在アクティブな（APIと同期すべき）生の注文オブジェクトを返します。
func (m *ManagedOrder) CurrentOrder() *order.Order {
	if m.Status <= StatusEntryActive {
		return m.Entry
	}
	if m.Exit != nil {
		return m.Exit
	}
	return m.Entry
}
