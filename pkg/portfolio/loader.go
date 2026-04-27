package portfolio

import (
	"encoding/json"
	"os"
)



// LoadFromJSON は、指定されたJSONファイルから監視銘柄リストを読み込みます。
//
// JSONファイルの形式例:
// [
//
//	{
//	  "symbol": "8306",
//	  "exchange": 3,
//	  "strategies": ["sample"],
//	  "sector": "銀行業"
//	}
//
// ]
func LoadFromJSON(path string) ([]SymbolTarget, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var targets []SymbolTarget
	if err := json.NewDecoder(file).Decode(&targets); err != nil {
		return nil, err
	}

	return targets, nil
}
