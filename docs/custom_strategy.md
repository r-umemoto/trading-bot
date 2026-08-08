# 💡 独自取引戦略の追加方法 (Implementing Custom Strategies)

本システムは、フレームワーク（取引エンジン）部と取引戦略（ロジック）部が疎結合に設計されており、**開発者が独自の取引戦略を自由に追加・拡張できるプラグイン構造**になっています。

取引戦略を追加・登録するには、以下の3つのステップに従います。

---

## 1. 取引戦略インターフェースの実装

独自のストラテジー型を定義し、[strategy.Strategy](file:///home/umemoto/trading-workspace/trading-bot/pkg/domain/sniper/strategy/strategy.go#L111) インターフェースの3つのメソッドを実装します。

```go
type Strategy interface {
	Name() string
	Evaluate(input StrategyInput) TargetPosition
	AnalysisLogger() *slog.Logger
}
```

* **`Name() string`**: ストラテジーのユニークな識別名（文字列）を返します。
* **`Evaluate(input StrategyInput) TargetPosition`**: 各時価（Tick）が到着するたびにシステムから呼び出される取引意思決定ロジックのコアです。引数の `StrategyInput`（現在価格、保有ポジション、現在値など）をもとに、目標とする保有数量 (`TargetPosition.Qty`) や指値価格などを判定して返します。
* **`AnalysisLogger() *slog.Logger`**: ストラテジー固有の解析・可視化ログ用のロガーを返します（不要な場合は `nil`）。

### 💡 注文キャンセルの制御
必要に応じて、[strategy.CancelChecker](file:///home/umemoto/trading-workspace/trading-bot/pkg/domain/sniper/strategy/strategy.go#L107) インターフェースを合わせて実装することで、未約定注文に対する動的なキャンセル可否判定（例: 一定時間経過後に指値をキャンセルする等）も実装可能です。
```go
type CancelChecker interface {
	ShouldCancel(input StrategyInput, ord *order.Order) bool
}
```

---

## 2. ファクトリの作成とシステムへの登録 (`strategy.Register`)

ストラテジーの初期化および注文実行ポリシーの選択を司る `StrategyFactory` インターフェースを実装します。

```go
type StrategyFactory interface {
	NewStrategy(detail symbol.Symbol, dataPool tick.DataPool, params interface{}) Strategy
	CreateExecutionPolicy(params interface{}) ExecutionPolicy
}
```

登録は、作成したパッケージの `init()` 関数内で `strategy.Register()` を呼び出すことで行います。これにより、指定された戦略名でファクトリがマップに登録されます。

### 実装コード例:
```go
package mystrategies

import (
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper/strategy"
	"github.com/r-umemoto/trading-bot/pkg/domain/symbol"
	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

type MyStrategy struct {
	// ストラテジー固有のメンバー変数（インジケーターや高値更新履歴など）
}

func (s *MyStrategy) Name() string { return "my_custom_strategy" }
func (s *MyStrategy) AnalysisLogger() *slog.Logger { return nil }
func (s *MyStrategy) Evaluate(input strategy.StrategyInput) strategy.TargetPosition {
	// 買い条件成立 -> Qty: 100 を返す
	// 売り条件成立 -> Qty: 0 を返す
	return strategy.TargetPosition{Qty: input.HoldQty()}
}

type MyStrategyFactory struct{}

func (f *MyStrategyFactory) NewStrategy(detail symbol.Symbol, dataPool tick.DataPool, params interface{}) strategy.Strategy {
	return &MyStrategy{}
}

func (f *MyStrategyFactory) CreateExecutionPolicy(params interface{}) strategy.ExecutionPolicy {
	// 標準ポリシー（NoopPolicy）やカスタムポリシーを返却
	return &strategy.NoopPolicy{}
}

func init() {
	// "my_custom_strategy" という識別名でこの戦略を登録
	strategy.Register("my_custom_strategy", &MyStrategyFactory{})
}
```

---

## 3. エントリポイントでのロード（サイドエフェクトインポート）

取引戦略を別リポジトリ（プライベートリポジトリなど）で開発し、このBotフレームワークを用いてビルド・実行する場合、`main.go` のインポートセクションで独自戦略のパッケージを**ブランクインポート（`_ "path/to/mystrategies"`）**します。

これにより、Goのランタイム起動時に `mystrategies` パッケージの `init()` 関数がトリガーされ、取引エンジンに自動的に戦略がロードされます。

**記述例 (`main.go`):**
```go
package main

import (
	"log"

	// 独自戦略パッケージの init() をトリガーさせて自動登録する
	_ "your-module/mystrategies"

	"github.com/r-umemoto/trading-bot/pkg/runner"
)

func main() {
	if err := runner.RunBot(); err != nil {
		log.Fatalf("❌ システム異常終了: %v", err)
	}
}
```

### 設定ファイルへの適用
登録した戦略を使用するには、[configuration.md](file:///home/umemoto/trading-workspace/trading-bot/docs/configuration.md) に従って、`configs/operations.json` 内の適用戦略リストに対象の登録名を指定します：
```json
"strategies": ["my_custom_strategy"]
```
これで、Bot起動時に自動であなたの独自戦略が銘柄に適用されて取引が開始されます。
