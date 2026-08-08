# Go-based Extensible Automated Trading Bot Framework

本プロジェクトは、日本の個別株取引（株ステーション API対応）を想定し、**リアルタイム時価・注文同期、プラグイン可能な取引戦略、バックテストシミュレータ、イベント駆動型のクラウド通知機能**を備えた、堅牢かつ拡張性の高い自動取引Botフレームワークです。

フリーランスのソフトウェアエンジニアとして、本番環境での長期安定稼働に耐えうる**「テスト容易性」「保守性」「資本安全性の担保」**を最重要課題とし、設計からインフラ選定まで徹底的にエンジニアリングのベストプラクティスを追求して開発しました。

---

## 🛠 採用技術・スタック (Tech Stack)

* **Core Language / Runtime**: Go 1.25 (並行処理を活かした低遅延イベント駆動アーキテクチャ)
* **Real-time Engine**: WebSocket / REST (証券会社APIとの双方向ストリーミング接続)
* **Design Patterns**: Clean Architecture, Dependency Inversion (DIP), Decorator (安全ラッパー群), Registry/Factory (戦略プラグイン)
* **Cloud Infrastructure (GCP)**: Firebase / Cloud Firestore, Eventarc, Cloud Functions v2 (Python / Tweepy)
* **DevOps & Testing**: Docker, Devcontainers, Docker Compose, Go Testing Framework (Mocking)

---

## 🎯 エンジニアリングアピールポイント (Key Technical Highlights)

1. **クリーンアーキテクチャをベースにした疎結合設計**
   変更の多いインフラ層（証券会社APIインターフェース等）と、ビジネスロジックを分離。DIP（依存性逆転）を適用し、本番APIクライアントとバックテスト用モックゲートウェイを同一ユースケース層から完全に透過的に扱えるように設計。
2. **デコレータ・パターンによる「資本安全性」の保護**
   誤発注や資産の喪失を防ぐため、取引戦略（Strategy）に対しデコレータ（Decorator）パターンを適用し、[WarmupStrategy](file:///home/umemoto/trading-workspace/trading-strategy-private/internal/strategies/warmup.go#L12)（開始直後の誤取引抑止）、[BudgetConstraint](file:///home/umemoto/trading-workspace/trading-strategy-private/internal/strategies/budget.go#L12)（予算オーバー保護）、[TradingTimeWindowWrapper](file:///home/umemoto/trading-workspace/trading-strategy-private/internal/strategies/trading_time_window_wrapper.go#L10)（取引時間外制御・大引け強制決済）などの多層的な保護ガードを実装。
3. **プラグイン可能な戦略設計（Registry / Factory パターン）**
   フレームワークコアを変更することなく、独自戦略を簡単に追加できる仕組み。設定ファイル（`operations.json`）から任意の戦略を名前指定で動的ロード可能。
4. **高精度なバックテストシミュレータ搭載**
   時系列時価データをミリ秒単位で再再生し、ネットワーク遅延や約定スリッページを忠実に再現可能なシミュレータ（Touch/Pessimistic/Volume約定モデル）を搭載。
5. **イベント駆動型のクラウド・ソーシャル連携 (GCP)**
   Firestoreのドキュメント変更をトリガーにEventarcとCloud Functions（Python）を経由して外部SNS（X）へレポートを自動配信するモダンなインフラ構成（GCP連携は任意で、未設定時は自動的にローカルCSV/JSON保存にフォールバック）。

---

## 📖 ドキュメントインデックス (Documentation Index)

詳細な機能仕様、アーキテクチャ、および各種手順については以下の各個別ドキュメントをご参照ください。

### 1. [📐 システム詳細設計・アーキテクチャ](file:///home/umemoto/trading-workspace/trading-bot/docs/architecture.md)
* クリーンアーキテクチャによる静的依存パッケージ図
* WebSocket時価受信から発注判定、成績保存までの動的シーケンス図
* デコレータパターンを応用した安全ラッパー（Warmup, Budget, TimeWindow）の解説
* Cloud Firestore/Eventarc/Cloud Functionsを用いたクラウド連携インフラ

### 2. [🚀 実行・導入手順 (Getting Started)](file:///home/umemoto/trading-workspace/trading-bot/docs/getting_started.md)
* 実行バイナリのビルド環境
* 環境変数（`.env`）の設定項目
* 本番/デモ取引の起動コマンド
* バックテストツールのパラメータ設定（約定モデル、擬似遅延の設定）

### 3. [⚙️ 構成設定 (Configuration Guide)](file:///home/umemoto/trading-workspace/trading-bot/docs/configuration.md)
* 監視銘柄マスタ定義（`configs/portfolio.json`）のスキーマおよび市場コード
* ストラテジーアサイン・作戦設定（`configs/operations.json`）のパラメータ仕様
* 環境変数やCLIフラグによるパス設定のカスタマイズと自動フォールバック

### 4. [💡 独自取引戦略の追加方法](file:///home/umemoto/trading-workspace/trading-bot/docs/custom_strategy.md)
* 取引判断インターフェース（`Strategy`）およびキャンセル制御（`CancelChecker`）の実装
* ファクトリの作成と `init()` での自動登録（`strategy.Register`）
* 別リポジトリからブランクインポート（サイドエフェクトインポート）で読み込む開発手順とコード例
