# 🚀 実行手順 (Getting Started)

本ドキュメントでは、Trading Botフレームワークのビルド、設定、および本番/バックテストの実行方法について解説します。

---

## 1. ビルド

Goのコンパイル機能を用いて、ターゲット環境（Windows/Linuxなど）に合わせた実行ファイルをビルドします。

**Windows環境向けビルド例:**
```bash
env GOOS=windows GOARCH=amd64 go build -o bot.exe ./cmd/bot
```

**Linux環境向けビルド例:**
```bash
go build -o bot ./cmd/bot
```

---

## 2. 環境変数の設定 (`.env`)

実行ファイルと同じディレクトリに `.env` ファイルを作成し、接続先ブローカー（証券会社API）の情報を設定します。

```env
BROKER_TYPE=kabu
KABU_API_URL=http://localhost:18081/kabusapi
KABU_PASSWORD=<あなたのパスワード>
```

### 設定項目
* `BROKER_TYPE`: `kabu` を設定します（auカブコム証券の株ステーションAPI対応）。
* `KABU_API_URL`: 接続先URL。検証環境（シミュレーション）は `http://localhost:18081/kabusapi`、本番環境は `http://localhost:18080/kabusapi` を指定します。
* `KABU_PASSWORD`: 株ステーションのAPIパスワードを設定します。

---

## 3. 本番/デモ取引の実行

環境変数のロード後、実行ファイルを実行して取引エンジンを起動します。

```powershell
# Windows PowerShell の場合
./bot.exe
```

```bash
# Linux / macOS の場合
./bot
```

---

## 4. バックテストの実行

過去のヒストリカル時価データ（CSVファイル）を用いて、戦略のシミュレーションを実行します。バックテストツールは `cmd/backtest` に実装されています。

**実行例:**
```bash
go run ./cmd/backtest -csv ./data/all_20260409.csv -portfolio ./configs/portfolio.json -operations ./configs/operations.json
```

### コマンドライン引数
* `-csv <path>`: バックテストに用いるヒストリカルTickデータ（CSV）のパス (デフォルト: `./data/all_20260409.csv`)。
* `-portfolio <path>`: ポートフォリオ設定ファイル（銘柄リスト）のパス。
* `-operations <path>`: 作戦設定ファイルのパス。
* `-execution-model <model>`: 約定シミュレーションのアルゴリズムモデル。以下の3つから指定します。
  * `touch`: 指値価格にTickが到達した時点で即座に全量約定とみなす簡易モデル。
  * `pessimistic` (デフォルト): 板状態や滑りを考慮し、実相場より厳しめに見積もる現実的な約定モデル。
  * `volume`: Tickの出来高（ボリューム）を消費させながら約定判定を行う高精度モデル。
* `-latency <ms>`: 発注・キャンセル時のネットワーク遅延（ミリ秒単位）をシミュレートする値 (例: `-latency 300` で 300ms の遅延を擬似挿入)。
* `-detailed-log`: 戦略内部状態（指標・ポジション・注文判定）の時系列 `.jsonl` 詳細ログ出力の有効化フラグ (デフォルト: `false`)。
  * **デフォルト (`false`)**: ディスクI/Oを完全に抑制（`io.Discard`）し、数百銘柄・多重タイムスケールの長期バックテストを数十倍〜100倍高速に実行します。
  * **有効化時 (`-detailed-log`)**: `./backtest_logs/<timestamp>/` 配下に 64KB バッファリングされた戦略詳細ログを出力します。特定の銘柄・特定日時のミリ秒〜秒単位のインジケーター推移やエントリー/エグジット判定理由を深掘り・可視化解析したい場合に指定してください。

---

## 5. 日次レポートのアップロード (Reporter)

Bot本体はトレード処理に集中するため、クラウドへの成績送信は行いません（ローカルの `./data/reports/daily_YYYY-MM-DD.json` に保存されます）。
大引け後（15:30以降など）に専用の `cmd/reporter` を実行して、成績データを Cloud Firestore へアップロードします。

**ビルド:**
```bash
go build -o bin/reporter ./cmd/reporter
```

**実行例:**
```bash
# 当日(JST)のレポートをアップロード
./bin/reporter

# 日付を指定してアップロード
./bin/reporter -date 2026-10-30

# 単一のレポートファイルを直接指定してアップロード
./bin/reporter -file ./data/reports/daily_2026-10-30.json

# ドライラン (Firestoreへ送信せず内容確認のみ)
./bin/reporter -date 2026-10-30 -dry-run
```

### コマンドライン引数
* `-date <YYYY-MM-DD>`: 対象日付 (未指定時は当日 JST)。
* `-file <path>`: 対象レポートJSONファイルパス (指定時は `-date` より優先)。
* `-dir <path>`: レポートJSON格納ディレクトリ (デフォルト: `./data/reports`)。
* `-project <id>`: GCPプロジェクトID (未指定時は環境変数 `GCP_PROJECT_ID` またはデフォルト値)。
* `-dry-run`: Firestoreへの書き込みを行わず読み込み確認のみ実施。
* `-verbose`: 詳細ログを出力。

---

## 6. トレードログの解析 (Analyzer)

Botが出力した `.jsonl` 形式のトレードログを解析し、勝率・損益・時間帯別成績・約定待ち時間などの詳細メトリクスを表示します。

**実行例:**
```bash
# 単一のログファイルを解析
go run ./cmd/analyzer -file ./data/logs/bot_20261030.jsonl

# ディレクトリ配下の全ログを一括解析
go run ./cmd/analyzer -dir ./data/logs
```
