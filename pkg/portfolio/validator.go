package portfolio

import (
	"fmt"
)

// MaxEnabledSymbols は kabuステーションAPI の登録可能上限数（50銘柄）を表します。
const MaxEnabledSymbols = 50

// Validate は portfolio.json と operations.json の設定整合性を検証します。
// kabuステーションAPIの制約（50銘柄上限）や、無効な銘柄への作戦割り当てを未然に防ぎます。
func Validate(targets []SymbolTarget, opTargets []OperationTarget) error {
	if len(targets) == 0 {
		return fmt.Errorf("ポートフォリオ (portfolio.json) が空です")
	}

	enabledCount := 0
	targetMap := make(map[string]SymbolTarget)
	seenSymbols := make(map[string]bool)

	for _, t := range targets {
		if t.Symbol == "" {
			return fmt.Errorf("portfolio.json にシンボルが空の銘柄が存在します: %+v", t)
		}
		if seenSymbols[t.Symbol] {
			return fmt.Errorf("portfolio.json に重複したシンボルが存在します: %s", t.Symbol)
		}
		seenSymbols[t.Symbol] = true
		targetMap[t.Symbol] = t

		if t.Enabled {
			enabledCount++
		}
	}

	if enabledCount > MaxEnabledSymbols {
		return fmt.Errorf("kabuステーションAPIの登録上限エラー: portfolio.json の enabled: true 銘柄数が %d 件です（最大%d件まで）", enabledCount, MaxEnabledSymbols)
	}
	if enabledCount == 0 {
		return fmt.Errorf("portfolio.json で enabled: true に設定されている銘柄が 0 件です")
	}

	// operations.json の作戦検証
	seenOps := make(map[string]bool)
	for _, op := range opTargets {
		if op.ID != "" {
			if seenOps[op.ID] {
				return fmt.Errorf("operations.json に重複した作戦IDが存在します: %s", op.ID)
			}
			seenOps[op.ID] = true
		}

		switch op.Type {
		case "default":
			symbolCode, _ := op.Params["symbol"].(string)
			strategiesRaw, _ := op.Params["strategies"].([]interface{})

			if len(strategiesRaw) > 0 {
				if symbolCode == "" {
					return fmt.Errorf("operations.json の作戦 (ID=%s) に symbol が指定されていません", op.ID)
				}
				target, exists := targetMap[symbolCode]
				if !exists {
					return fmt.Errorf("作戦 (ID=%s) の対象銘柄 %s が portfolio.json に登録されていません", op.ID, symbolCode)
				}
				if !target.Enabled {
					return fmt.Errorf("作戦 (ID=%s) の対象銘柄 %s は portfolio.json で無効 (enabled: false) に設定されています", op.ID, symbolCode)
				}
			}

		case "pair_trading":
			symbolA, _ := op.Params["symbol_a"].(string)
			symbolB, _ := op.Params["symbol_b"].(string)

			for _, sym := range []string{symbolA, symbolB} {
				if sym == "" {
					return fmt.Errorf("ペアトレード作戦 (ID=%s) に銘柄が指定されていません", op.ID)
				}
				target, exists := targetMap[sym]
				if !exists {
					return fmt.Errorf("ペアトレード作戦 (ID=%s) の対象銘柄 %s が portfolio.json に登録されていません", op.ID, sym)
				}
				if !target.Enabled {
					return fmt.Errorf("ペアトレード作戦 (ID=%s) の対象銘柄 %s は portfolio.json で無効 (enabled: false) に設定されています", op.ID, sym)
				}
			}
		}
	}

	return nil
}
