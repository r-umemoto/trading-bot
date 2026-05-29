package portfolio

import (
	"encoding/json"
	"os"
)

// OperationTarget は operations.json の各作戦設定を表す構造体です。
type OperationTarget struct {
	Type   string                 `json:"type"`   // 作戦タイプ (例: "pair_trading")
	ID     string                 `json:"id"`     // 作戦のユニークID (例: "PairOp_7201_7267")
	Params map[string]interface{} `json:"params"` // パラメータ (例: symbol_a, symbol_b, threshold, qty)
}

// LoadOperationsFromJSON は指定されたJSONファイルから作戦設定リストを読み込みます。
func LoadOperationsFromJSON(path string) ([]OperationTarget, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var targets []OperationTarget
	if err := json.NewDecoder(file).Decode(&targets); err != nil {
		return nil, err
	}

	return targets, nil
}
