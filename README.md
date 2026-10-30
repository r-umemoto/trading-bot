# Go-based Extensible Automated Trading Bot Framework

本プロジェクトは、日本の個別株取引（株ステーション API対応）を想定し、**リアルタイム時価・注文同期、プラグイン可能な取引戦略、バックテストシミュレータ、イベント駆動型のクラウド通知機能**を備えた、堅牢かつ拡張性の高い自動取引Botフレームワークです。

フリーランスのソフトウェアエンジニアとして、本番環境での長期安定稼働に耐えうる**「テスト容易性」「保守性」「資本安全性の担保」**を最重要課題とし、設計からインフラ選定まで徹底的にエンジニアリングのベストプラクティスを追求して開発しました。

---

## 🛠 採用技術・スタック (Tech Stack)

* **Core Language / Runtime**: Go 1.25 (並行処理を活かした低遅延イベント駆動アーキテクチャ)
* **Real-time Engine**: WebSocket / REST (証券会社APIとの双方向ストリーミング接続)
* **Design Patterns**: Clean Architecture, Dependency Inversion (DIP), Registry/Factory (戦略プラグイン)
* **Cloud Infrastructure (GCP)**: Firebase / Cloud Firestore, Eventarc, Cloud Functions v2 (Python / Tweepy)
* **DevOps & Testing**: Docker, Devcontainers, Docker Compose, Go Testing Framework (Mocking)

---

## 🎯 エンジニアリングアピールポイント (Key Technical Highlights)

1. **クリーンアーキテクチャをベースにした疎結合設計**
   変更の多いインフラ層（証券会社APIインターフェース等）と、ビジネスロジックを分離。DIP（依存性逆転）を適用し、本番APIクライアントとバックテスト用モックゲートウェイを同一ユースケース層から完全に透過的に扱えるように設計。
2. **自己復旧と頑健性を備えた注文制御（ゾンビ注文自己修復 & クリーナー）**
   通信障害等による注文キャンセル状態の膠着（ゾンビ注文）を自動検知。バックグラウンドでブローカーAPIへポーリング照会をかけて最新状態を能動取得し、システムステートを自動回復するセルフヒーリング機構を実装。また、起動・終了時に残存している未約定注文の強制キャンセルと保有建玉の一括決済を自動で行いシステムの初期状態の安全を保証する [PositionCleaner](./pkg/usecase/position_cleaner.go) を搭載。
3. **プラグイン可能な戦略設計（Registry / Factory パターン）**
   フレームワークコアを変更することなく、独自戦略を簡単に追加できる仕組み。設定ファイル（`operations.json`）から任意の戦略を名前指定で動的ロード可能。
4. **高精度な約定・遅延シミュレータ（ExecutionPolicy）**
   ヒストリカルTickデータ（CSV）をミリ秒単位で再生するバックテストエンジン。板情報をシミュレートする約定モデル（Touch/Pessimistic/Volume）に加え、ネットワーク/ブローカー遅延（ミリ秒）の再現機能をサポート。さらに、疑似約定の待機時間を制御する [TouchTTLPolicy](./pkg/domain/sniper/strategy/policy.go) などの各種 [ExecutionPolicy](./pkg/domain/sniper/strategy/policy.go) により、微細な価格変動による無駄なキャンセル・再発注（注文スパム）を抑制。
5. **責務分離とイベント駆動型のクラウド・ソーシャル連携 (GCP)**
   Bot本体をトレード処理に特化させ、クラウド依存（Firestore接続等）を完全に排除。Botが出力した日次レポートやログを独立したアップローダー（`reporter`）がFirestoreへ連携し、EventarcとCloud Functions（Python）を経由して外部SNS（X）へ配信する堅牢な分離構成。

---

## 📦 アプリケーション・実行ファイル構成 (Applications)

本プロジェクトはマルチバイナリ構成を採用しており、用途に応じた独立した実行ファイルをビルドできます。

| コマンド | ディレクトリ | 役割 |
| :--- | :--- | :--- |
| **Bot** | `cmd/bot` | **取引実行エンジン**。外部ネットワーク障害の影響を受けないよう、クラウド通信を排しローカル完結で高速にトレードとログ出力を実行します。 |
| **Reporter** | `cmd/reporter` | **レポート集計 & Firestore アップローダー**。大引け後などに実行し、Botが出力した日次レポートを読み取って Cloud Firestore へ送信します。 |
| **Analyzer** | `cmd/analyzer` | **ログ解析CLI**。Botが出力したトレードログ（`.jsonl`）を解析し、銘柄・戦略・時間帯別の勝率や約定待ち時間などをターミナルに可視化します。 |
| **Backtest** | `cmd/backtest` | **高速バックテストツール**。ヒストリカルTickデータ（CSV）を用いて取引戦略の損益・約定シミュレーションを実行します。 |

### ビルド例
```bash
# 全バイナリまたは必要なバイナリを個別にビルド
go build -o bin/bot ./cmd/bot
go build -o bin/reporter ./cmd/reporter
go build -o bin/analyzer ./cmd/analyzer
go build -o bin/backtest ./cmd/backtest
```

---

## 📖 ドキュメントインデックス (Documentation Index)

詳細な機能仕様、アーキテクチャ、および各種手順については以下の各個別ドキュメントをご参照ください。

### 1. [📐 システム詳細設計・アーキテクチャ](./docs/architecture.md)
* クリーンアーキテクチャによる静的依存パッケージ図
* WebSocket時価受信から発注判定、成績保存までの動的シーケンス図
* 自己復旧・注文制御（ゾンビ注文修復、約定ポリシー、ポジションクリーナー）の解説
* Cloud Firestore/Eventarc/Cloud Functionsを用いたクラウド連携インフラ

### 2. [🚀 実行・導入手順 (Getting Started)](./docs/getting_started.md)
* 実行バイナリのビルド環境
* 環境変数（`.env`）の設定項目
* 本番/デモ取引の起動コマンド
* バックテストツールのパラメータ設定（約定モデル、擬似遅延の設定）

### 3. [⚙️ 構成設定 (Configuration Guide)](./docs/configuration.md)
* 監視銘柄マスタ定義（`configs/portfolio.json`）のスキーマおよび市場コード
* ストラテジーアサイン・作戦設定（`configs/operations.json`）のパラメータ仕様
* 環境変数やCLIフラグによるパス設定のカスタマイズと自動フォールバック

### 4. [💡 独自取引戦略の追加方法](./docs/custom_strategy.md)
* 取引判断インターフェース（`Strategy`）およびキャンセル制御（`CancelChecker`）の実装
* ファクトリの作成と `init()` での自動登録（`strategy.Register`）
* 別リポジトリからブランクインポート（サイドエフェクトインポート）で読み込む開発手順とコード例
