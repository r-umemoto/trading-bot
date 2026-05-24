package sniper

import (
	"time"
	"github.com/r-umemoto/trading-bot/pkg/domain/order"
)

// ManagedStatus は論理注文（ManagedOrder）のライフサイクル状態を表します
type ManagedStatus int

const (
	StatusEntryActive   ManagedStatus = iota // エントリー中
	StatusExitPreparing                      // エントリー約定・決済注文発射準備（SyncOrdersが担当）
	StatusExitActive                         // 決済中
	StatusCompleted                          // すべて完了
	StatusCanceled                           // キャンセル終了
)

// ManagedOrder は一つのトレード（エントリーから決済まで）を管理する論理的な単位です。
type ManagedOrder struct {
	ID    string
	Entry *order.Order    // エントリー注文
	Exit  *order.Order    // 決済注文（IFDの場合に使用）

	Status ManagedStatus
}

// NewManagedOrder は新しい論理注文を生成します。
func NewManagedOrder(id string, entry *order.Order, exit *order.Order) *ManagedOrder {
	return &ManagedOrder{
		ID:    id,
		Entry: entry,
		Exit:  exit,
		Status: StatusEntryActive,
	}
}

// IsCompleted はこの論理注文が完全に終了したかどうかを判定します。
func (m *ManagedOrder) IsCompleted() bool {
	return m.Status == StatusCompleted || m.Status == StatusCanceled
}

// CurrentOrder は現在アクティブな（APIと同期すべき）生の注文オブジェクトを返します。
func (m *ManagedOrder) CurrentOrder() *order.Order {
	if m.Status == StatusEntryActive {
		return m.Entry
	}
	if m.Exit != nil {
		return m.Exit
	}
	return m.Entry
}

// CheckTimeout は現在の注文が疑似約定タイムアウトしていないか確認し、必要なら状態を戻します。
func (m *ManagedOrder) CheckTimeout(now time.Time) bool {
	curr := m.CurrentOrder()
	if curr != nil && curr.Status == order.ORDER_STATUS_FILL_EXPECTED {
		if !curr.Synthetic.ExpectedAt.IsZero() && now.Sub(curr.Synthetic.ExpectedAt) > 20*time.Second {
			curr.Status = order.ORDER_STATUS_IN_PROGRESS
			return true
		}
	}
	return false
}
