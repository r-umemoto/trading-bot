package order

import (
	"sort"
	"time"
)

// ExecutionDetail は新たに検知された約定の事実を表します。
type ExecutionDetail struct {
	Execution      Execution
	Action         Action
	OrderCreatedAt time.Time
	ParentOrder    *Order
}

// ExecutionDetails は ExecutionDetail のスライスに対するカスタム型で、ドメイン知識に関する操作を提供します。
type ExecutionDetails []ExecutionDetail

// Sort は約定明細を約定時間の時系列順（古い順）にソートします。
func (eds ExecutionDetails) Sort() {
	sort.Slice(eds, func(i, j int) bool {
		return eds[i].Execution.ExecutionTime.Before(eds[j].Execution.ExecutionTime)
	})
}

// ReconcileOrders は、ローカルで追跡しているアクティブな注文リストと取引所APIから取得した最新注文リストを
// 突き合わせ、最新の状態を反映した注文リストを生成して返します（純粋関数）。
// 同時に、まだ処理されていない新規約定レコード（ExecutionDetail）のリストを時系列順にソートして返します。
func ReconcileOrders(
	localOrders []*Order,
	apiOrders Orders,
	symbolCode string,
	processedExecutionIDs map[string]bool,
	now time.Time,
) ([]*Order, ExecutionDetails) {
	var reconciledOrders []*Order

	// 1. 未完了注文の保持（APIへの反映待ち含む）
	for _, o := range localOrders {
		if o.IsPending() {
			if now.Sub(o.CreatedAt) < 30*time.Second {
				reconciledOrders = append(reconciledOrders, o)
			}
		} else if !o.IsCompleted() {
			reconciledOrders = append(reconciledOrders, o)
		}
	}

	var pendingExecs ExecutionDetails

	// 2. APIレポートの反映
	for _, ext := range apiOrders.Orders {
		if ext.Symbol != symbolCode {
			continue
		}

		var matchedInternal *Order
		// 現在のアクティブな注文リストから探す
		for _, o := range reconciledOrders {
			if o.ID == ext.ID {
				matchedInternal = o
				break
			}
		}
		// 見つからない場合は、渡されたローカル注文全体から探す（直近で完了した可能性があるもの）
		if matchedInternal == nil {
			for _, o := range localOrders {
				if o.ID == ext.ID {
					matchedInternal = o
					break
				}
			}
		}

		if matchedInternal == nil {
			continue
		}

		// 状態同期
		if matchedInternal.IsFillExpected() && !ext.IsCompleted() {
			// 疑似約定状態を維持し、CumQtyのみ同期する
			matchedInternal.CumQty = ext.CumQty
		} else if matchedInternal.IsCancelSent() && !ext.IsCompleted() {
			// キャンセル送信中状態を維持し、カブコム側の未完了状態（WAITING等）に引き戻されないようにする
			matchedInternal.CumQty = ext.CumQty
		} else {
			matchedInternal.TransitionToStatus(ext.Status())
			matchedInternal.CumQty = ext.CumQty
		}
		if matchedInternal.IsPending() {
			matchedInternal.ToActive()
		}

		// 約定の抽出
		for _, exec := range ext.Executions {
			if !processedExecutionIDs[exec.ID] {
				pendingExecs = append(pendingExecs, ExecutionDetail{
					Execution:      exec,
					Action:         matchedInternal.Action,
					OrderCreatedAt: matchedInternal.CreatedAt,
					ParentOrder:    matchedInternal,
				})
			}
		}
	}

	// 3. 約定の反映（時系列順）
	pendingExecs.Sort()

	// 4. 完了した注文をアクティブ注文リストから除外。
	// ただし、完全約定(FILLED)であっても、未解決のIfDone決済子注文を保持している場合は、
	// 将来取引所から届く決済子注文との親子関係の解決を行うためにアクティブ注文リストに残します。
	var activeOrders []*Order
	for _, o := range reconciledOrders {
		if !o.IsCompleted() || (o.IsFilled() && o.IfDone != nil && o.IfDone.IsPending()) {
			activeOrders = append(activeOrders, o)
		}
	}

	return activeOrders, pendingExecs
}
