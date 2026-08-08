# ⚙️ 構成設定 (Configuration Guide)

本ドキュメントでは、Botの監視銘柄マスタ定義（`portfolio.json`）および、各銘柄に戦略を割り当てる作戦定義（`operations.json`）の仕様について解説します。

---

## 1. 監視銘柄設定 (`configs/portfolio.json`)

取引候補となる銘柄マスタをJSON配列で定義します。Botはこのファイルから銘柄情報および取引市場を取得します。

### 設定項目 (SymbolTarget)
* `symbol` (string): 4桁の銘柄コード (例: `"8306"`, `"7201"`)。
* `name` (string): 銘柄名。ログやレポートの表示用の任意項目 (例: `"三菱UFJ"`)。
* `exchange` (string): 注文を発注する市場区分。以下のいずれかの文字列を指定します：
  * `"TOSHO"`: 東京証券取引所
  * `"SOR"`: SOR (Smart Order Routing)
  * `"TOSHO_PLUS"`: 東証プラス（株ステーション用の特別な市場区分）
  * `"NONE"`: 指定なし（デフォルト）
* `sector` (string): セクター・業種名。分析用の任意項目 (例: `"銀行業"`)。
* `enabled` (boolean): `true` に設定した銘柄のみが、Botの監視および取引実行の有効対象になります。**デフォルトのテンプレートではすべて `false`（または未定義によるデフォルト `false`）になっているため、取引を行いたい銘柄は明示的に `"enabled": true` を設定してください。**

**記述例:**
```json
[
  {
    "symbol": "8306",
    "name": "三菱UFJフィナンシャル・グループ",
    "exchange": "TOSHO_PLUS",
    "sector": "銀行業",
    "enabled": true
  },
  {
    "symbol": "7201",
    "name": "日産自動車",
    "exchange": "TOSHO_PLUS",
    "sector": "輸送用機器",
    "enabled": true
  }
]
```

---

## 2. 作戦設定 (`configs/operations.json`)

監視対象に選んだ有効な銘柄に対して、どのような戦略・ルールで取引（作戦）を実行するかを設定します。作戦はスナイパー（取引エージェント）をグループ化して制御する単位です。

### 設定項目 (OperationTarget)
* `type` (string): 作戦のカテゴリ。以下のいずれかを指定します。
  * `"default"`: 単一銘柄での通常の取引作戦。
  * `"pair_trading"`: サヤ取りなどのペアトレード作戦。
* `id` (string): 作戦を識別するユニークなID (例: `"DefaultOp_8306"`, `"PairOp_7201_7267"`)。
* `params` (object): 作戦タイプごとに必要なパラメータ。

#### 💡 `"type": "default"` の場合に必要なパラメータ
* `symbol` (string): 対象の銘柄コード。
* `strategies` (array of string): 適用する戦略名 (例: `["sample"]`)。
* `strategy_params` (object): 戦略ごとのカスタムパラメータ (任意)。

#### 💡 `"type": "pair_trading"` の場合に必要なパラメータ
* `symbol_a` (string): 銘柄Aのコード。
* `symbol_b` (string): 銘柄Bのコード。
* `threshold` (number): サヤ（価格比など）の判定しきい値。
* `qty` (number): 取引する株数（数量）。

**記述例:**
```json
[
  {
    "type": "default",
    "id": "DefaultOp_8306",
    "params": {
      "symbol": "8306",
      "strategies": ["sample"]
    }
  },
  {
    "type": "pair_trading",
    "id": "PairOp_7201_7267",
    "params": {
      "symbol_a": "7201",
      "symbol_b": "7267",
      "threshold": 1.5,
      "qty": 100.0
    }
  }
]
```

> [!TIP]
> **未割当銘柄の自動フォールバック機能**
> `portfolio.json` で `enabled` を `true` に設定しているにもかかわらず、`operations.json` で明示的に作戦が定義されていない銘柄がある場合、Bot起動時に自動的に `FallbackOp_<銘柄コード>` という名前のデフォルト作戦として自動配備され、稼働します。

---

## 3. パス設定のカスタマイズ

設定ファイルの読み込みパスは、デフォルト（`configs/portfolio.json` / `configs/operations.json`）から任意の場所へ上書き変更することが可能です。

### 環境変数によるパス指定 (本番・デバッグ用)
本番Botを実行する際、以下の環境変数を定義することで読み込みパスを切り替えられます。
* `PORTFOLIO_PATH`: ポートフォリオ設定ファイルのカスタムパス
* `OPERATIONS_PATH`: 作戦設定ファイルのカスタムパス

### コマンドライン引数によるパス指定 (バックテスト用)
バックテストツール (`cmd/backtest`) では、起動パラメータでパスを直接指定できます。
* `-portfolio <path>`: ポートフォリオJSONファイルのパス (デフォルト: `./configs/portfolio.json`)
* `-operations <path>`: 作戦設定JSONファイルのパス (デフォルト: `./configs/operations.json`)
