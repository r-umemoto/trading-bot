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
