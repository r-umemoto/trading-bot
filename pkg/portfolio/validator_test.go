package portfolio

import (
	"fmt"
	"testing"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name      string
		targets   []SymbolTarget
		opTargets []OperationTarget
		wantErr   bool
	}{
		{
			name: "正常系: 50銘柄以内かつ作戦対象がすべて有効",
			targets: []SymbolTarget{
				{Symbol: "8306", Enabled: true},
				{Symbol: "7203", Enabled: true},
				{Symbol: "9984", Enabled: false},
			},
			opTargets: []OperationTarget{
				{
					ID:   "Op_8306",
					Type: "default",
					Params: map[string]interface{}{
						"symbol":     "8306",
						"strategies": []interface{}{"momentum"},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "エラー: enabled が 50 銘柄を超える",
			targets: func() []SymbolTarget {
				var list []SymbolTarget
				for i := 1; i <= 51; i++ {
					list = append(list, SymbolTarget{Symbol: fmt.Sprintf("SYM%03d", i), Enabled: true})
				}
				return list
			}(),
			opTargets: nil,
			wantErr:   true,
		},
		{
			name: "エラー: 作戦対象銘柄が portfolio.json で enabled: false",
			targets: []SymbolTarget{
				{Symbol: "8306", Enabled: false},
			},
			opTargets: []OperationTarget{
				{
					ID:   "Op_8306",
					Type: "default",
					Params: map[string]interface{}{
						"symbol":     "8306",
						"strategies": []interface{}{"momentum"},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "エラー: 作戦対象銘柄が portfolio.json に未登録",
			targets: []SymbolTarget{
				{Symbol: "7203", Enabled: true},
			},
			opTargets: []OperationTarget{
				{
					ID:   "Op_8306",
					Type: "default",
					Params: map[string]interface{}{
						"symbol":     "8306",
						"strategies": []interface{}{"momentum"},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "エラー: portfolio.json に重複シンボルが存在",
			targets: []SymbolTarget{
				{Symbol: "8306", Enabled: true},
				{Symbol: "8306", Enabled: true},
			},
			opTargets: nil,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.targets, tt.opTargets)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
