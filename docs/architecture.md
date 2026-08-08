# 📐 システム詳細設計・アーキテクチャ (Architecture & Design)

本ドキュメントでは、Trading Botフレームワークのモジュール構造、時価判定から発注までの処理フロー、クラウド連携インフラ、および資本保護を実現する設計パターンについて解説します。

---

## 1. 静的構造（パッケージ依存関係）

本システムは、モジュール間の結合度を極限まで低く保ち、検証性（テスト容易性）と拡張性を高めるため、**クリーンアーキテクチャ (Clean Architecture)** を設計基盤に採用しています。

依存関係は外側から内側（ドメインコア）に向かって一方向のみに流れる原則（Dependency Rule）を徹底しており、インフラ（外部API）を変更してもコアビジネスロジックは影響を受けない設計になっています。

```mermaid
graph TD
    %% スタイルの定義
    classDef infra fill:#f9d5e5,stroke:#333,stroke-width:2px;
    classDef runner fill:#eeeeee,stroke:#333,stroke-width:2px;
    classDef usecase fill:#d9e2ec,stroke:#333,stroke-width:2px;
    classDef domain fill:#bac7a7,stroke:#333,stroke-width:2px;
    classDef config fill:#ffe3ed,stroke:#333,stroke-width:2px;

    subgraph Runner_Setup ["🚀 実行・制御レイヤー (Runner & Setup)"]
        CMD_Bot["cmd/bot"]
        CMD_Backtest["cmd/backtest"]
        Engine["pkg/engine (Setup/Build)"]
    end

    subgraph Interface_Adapters ["🔌 インフラ・外部接続レイヤー (Infrastructure)"]
        Kabu_API["pkg/infra/kabu (kabu.com API Client)"]
        Firestore_Rep["pkg/infra/report (Firestore Repository)"]
        Local_Rep["pkg/infra/report (Local File Repository)"]
    end

    subgraph Application_Business_Rules ["⚙️ ユースケースレイヤー (Usecase)"]
        Handler["pkg/usecase (UseCaseHandler - Facade)"]
        System_UC["pkg/usecase (SystemUseCase)"]
        Trade_UC["pkg/usecase (TradeUseCase)"]
        Cleaner["pkg/usecase (PositionCleaner)"]
    end

    subgraph Enterprise_Business_Rules ["🎯 ドメインレイヤー (Domain Core)"]
        Gateway_IF["pkg/domain/market (MarketGateway - Interface)"]
        Report_IF["pkg/domain/report (Repository - Interface)"]
        
        subgraph Domain_Aggregates ["Domain Aggregates & Models"]
            Operation["pkg/domain/sniper (Operation / PairTrading)"]
            Sniper["pkg/domain/sniper (Sniper Agent)"]
            Strategy["pkg/domain/sniper/strategy (Evaluate / IfDone)"]
            DataPool["pkg/domain/tick (DataPool / Indicators)"]
            Order["pkg/domain/order (Order / Ticket)"]
            Position["pkg/domain/position (Holding State)"]
        end
    end

    subgraph Configuration ["⚙️ 設定管理"]
        Config["pkg/config"]
        Portfolio["pkg/portfolio"]
    end

    %% 依存関係の矢印 (外側から内側へ)
    CMD_Bot --> Engine
    CMD_Backtest --> Engine
    
    Engine --> Handler
    Engine --> Kabu_API
    Engine --> Firestore_Rep
    Engine --> Local_Rep
    Engine --> Config
    Engine --> Portfolio

    Handler --> System_UC
    Handler --> Trade_UC

    System_UC --> Cleaner
    System_UC --> Gateway_IF
    System_UC --> Operation

    Trade_UC --> Gateway_IF
    Trade_UC --> Report_IF
    Trade_UC --> Operation
    Trade_UC --> DataPool

    Cleaner --> Gateway_IF

    %% インフラはインターフェースを実装 (DIP - 依存性逆転の原則)
    Kabu_API -.->|Implements| Gateway_IF
    Firestore_Rep -.->|Implements| Report_IF
    Local_Rep -.->|Implements| Report_IF

    %% ドメイン内部の関係
    Operation --> Sniper
    Sniper --> Strategy
    Sniper --> Order
    Sniper --> Position
    Strategy --> DataPool

    %% クラス割り当て
    class CMD_Bot,CMD_Backtest,Engine runner;
    class Kabu_API,Firestore_Rep,Local_Rep infra;
    class Handler,System_UC,Trade_UC,Cleaner usecase;
    class Gateway_IF,Report_IF,Operation,Sniper,Strategy,DataPool,Order,Position domain;
    class Config,Portfolio config;
```

### 依存関係逆転の原則 (DIP) によるモック化
インフラと接続するゲートウェイ部分を `MarketGateway` インターフェースで抽象化しているため、実機の株ステーション接続用クライアント（[pkg/infra/kabu](file:///home/umemoto/trading-workspace/trading-bot/pkg/infra/kabu/market_gateway.go)）と、バックテスト用のインメモリ再生シミュレータ（[pkg/infra/backtest](file:///home/umemoto/trading-workspace/trading-bot/pkg/infra/backtest/gateway.go)）を、ユースケース層を一切変更することなく差し替えて実行することができます。

---

## 2. 動的処理フロー（時価受信から判定・発注まで）

WebSocketによるリアルタイム株価データ受信（Tickイベント）を起点として、テクニカル分析指標の計算、戦略評価、発注処理、取引成績の記録にいたる一連のライフサイクル・データフローです。

```mermaid
sequenceDiagram
    autonumber
    actor Broker as 証券取引所 (kabu.com API)
    participant Gateway as MarketGateway<br/>(pkg/infra/kabu)
    participant TradeUC as TradeUseCase<br/>(pkg/usecase)
    participant Op as Operation<br/>(pkg/domain/sniper)
    participant Sniper as Sniper Agent<br/>(pkg/domain/sniper)
    participant Strat as Strategy<br/>(pkg/domain/sniper/strategy)
    participant DataPool as DataPool / Indicators<br/>(pkg/domain/tick)
    participant Repo as Report Repository<br/>(Firestore / Local)

    %% 1. 初期化と市場データの監視開始
    Note over TradeUC, Gateway: システム起動時にストリーミング接続を初期化
    TradeUC->>Gateway: Listen(ctx)
    Gateway-->>TradeUC: Ticks / Orders イベントチャネルを返却

    %% 2. Tick 受信と評価
    Broker->>Gateway: [WebSocket] リアルタイム株価配信 (Tick)
    Gateway->>TradeUC: Ticks チャネル経由で配信 (tick.Tick)
    TradeUC->>Op: HandleTick(tick)

    %% 3. テクニカル評価と戦略の実行
    Op->>Sniper: Tick判定・保有ポジション情報を転送
    Sniper->>DataPool: 時系列データに追加 & インジケータ計算 (RSI / Sigma等)
    Sniper->>Strat: Evaluate(StrategyInput)
    Strat->>DataPool: インジケータ値の参照
    Strat-->>Sniper: 判断シグナル (Buy / Sell / Hold) & 注文理由 (Reason)

    %% 4. 注文作成とIfDone設定
    alt シグナルが Buy または Sell
        Sniper->>Strat: IfDone(simulatedInput)
        Strat-->>Sniper: 決済用の「次の意図」を決定 (利確・損切・トレイリング)
        Sniper->>Sniper: ローカル注文ペア (Entry & Exit/IfDone) を構築
        Sniper-->>Op: 射出可能オブジェクト (Bullet) を返却
        Op-->>TradeUC: 発注アクションを伝達 (Bullet)
        
        %% 5. 発注実行
        TradeUC->>Gateway: SendOrder(ctx, orderRequest)
        Gateway->>Broker: API経由で新規注文を送信
        Broker-->>Gateway: 注文受付成功 (取引所ID)
        Gateway-->>TradeUC: 更新された注文情報 (Order)
        TradeUC->>Op: UpdateOrderID (ローカルIDから取引所IDへ紐付け更新)
    end

    %% 6. 約定および取引成績の保存
    Broker->>Gateway: [WebSocket/Polling] 約定確定 / 注文ステータス更新
    Gateway->>TradeUC: Orders チャネル経由で配信 (order.Orders)
    TradeUC->>Op: UpdateOrders(orders)
    Op->>Sniper: 注文状態の最終同期

    %% 7. レポート出力
    opt システム終了時または一定周期
        TradeUC->>Repo: Save(dailyReport)
        Note over Repo: Firestore またはローカル JSON に成績を永続化
    end
```

---

## 3. 安全性を支える設計パターン (Decorator Pattern)

実資金を市場で運用するにあたり、最も重視すべきは**「想定外のバグや通信断による不要・無謀な注文の徹底防止（資本保護）」**です。

本システムでは、すべての取引戦略に対し**デコレータ（Decorator）パターン**を適用して安全ラッパーを実装しており、個別の取引ロジック自体に複雑な保護コードを書くことなく、透過的に安全ガードを何重にも構築できる仕組みをとっています。

### 🛡️ ウォームアップストッパー ([WarmupStrategy](file:///home/umemoto/trading-workspace/trading-strategy-private/internal/strategies/warmup.go#L12))
* **目的**: 起動直後の数十分間など、テクニカル分析指標（移動平均など）を正しく計算するためのデータ履歴（時系列Tick）が十分に蓄積されていない期間中の誤判定を防ぎます。
* **動作**: 起動から指定時間内（例: 5分）かつノーポジションである場合、ベース戦略がどのような注文判断を行おうとも強制的に `HOLD` を返して新規エントリーをブロックします。

### 💵 予算制御プロキシ ([BudgetConstraint](file:///home/umemoto/trading-workspace/trading-strategy-private/internal/strategies/budget.go#L12))
* **目的**: システムバグによる無限ナンピン発注や、注文バッチ重複による資金キャパシティ超過を完全に防止します。
* **動作**: 現在の評価額およびベース戦略が提示した発注数量に伴う「概算必要投資額」を計算し、あらかじめ設定した最大投資予算を超える発注をすべて強制的に `HOLD`（ポジション維持）にクランプします。

### 🕒 時間窓・大引け制御 ([TradingTimeWindowWrapper](file:///home/umemoto/trading-workspace/trading-strategy-private/internal/strategies/trading_time_window_wrapper.go#L10))
* **目的**: 取引対象外の時間帯の誤注文を完全に制限し、大引け前（例: 15:25）に強制手仕舞いを行ってオーバーナイト（持越し）リスクを排除します。
* **動作**:
  * 取引可能時間（例: 前場9:00-11:30、後場12:30-15:00）以外の時間帯は強制的にブラックアウトして新規買い・売りを遮断します。
  * 設定した大引け前決済時刻（例: 15:25）以降にポジションを保有している場合、強制的にすべてのポジションを市場注文でクローズさせます。

---

## 4. クラウドインフラ連携（システム全体像）

Trading Botが本番環境で動いた際、どのようにクラウドサービス（Google Cloud Platform: GCP）や外部APIと連携するのかを示した構成図です。

```mermaid
graph LR
    %% スタイルの定義
    classDef goApp fill:#00ADD8,stroke:#333,stroke-width:2px,color:#fff;
    classDef gcp fill:#4285F4,stroke:#333,stroke-width:2px,color:#fff;
    classDef ext fill:#1DA1F2,stroke:#333,stroke-width:2px,color:#fff;
    classDef file fill:#eceff1,stroke:#333,stroke-width:2px;

    subgraph Local_Execution_Environment ["💻 実行環境 (Local PC / VM)"]
        ENV_File[".env ファイル"] -->|設定ロード| GO_Bot
        Config_JSON["configs/portfolio.json<br/>configs/operations.json"] -->|マスタ読み込み| GO_Bot
        
        GO_Bot["🤖 Go Trading Bot<br/>(pkg/engine)"]
        
        GO_Bot -->|CSV出力| CSV_File["data/reports/*.csv"]
        GO_Bot -->|ロギング| Log_File["logs/YYYYMMDD/*.jsonl"]
    end

    subgraph Kabu_Station ["🏢 証券会社インフラ"]
        Kabu_API["株ステーション API<br/>(kabusapi: 18080/18081)"]
    end

    subgraph GCP_Cloud_Infrastructure ["☁️ Google Cloud Platform (GCP)"]
        Firestore[("🔥 Cloud Firestore<br/>(daily_reports コレクション)")]
        Eventarc["⚡ Eventarc<br/>(Firestore Document Event)"]
        CloudFunction["🐍 Cloud Functions (v2)<br/>(Python: post_to_x)"]
    end

    subgraph External_Services ["🌐 外部ソーシャルメディア"]
        X_API["🐦 X (Twitter) API v2"]
    end

    %% 接続関係の定義
    GO_Bot <-->|REST API & WebSocket| Kabu_API
    GO_Bot -->|環境変数にGCP認証情報がある場合| Firestore
    
    Firestore -->|ドキュメント作成/更新トリガー| Eventarc
    Eventarc -->|イベント転送 - Protobufデシリアライズ| CloudFunction
    CloudFunction -->|Tweepy経由でツイート投稿| X_API

    %% クラス割り当て
    class GO_Bot goApp;
    class Firestore,Eventarc,CloudFunction gcp;
    class Kabu_API,X_API ext;
    class ENV_File,Config_JSON,CSV_File,Log_File file;
```

GCP（Firestore/Eventarc）および X（Twitter）APIとの連携は**完全に任意**です。環境変数 `GOOGLE_APPLICATION_CREDENTIALS` が設定されていない場合、Botは自動的に**ローカル保存モード（`./data/reports/` 配下へのJSON/CSV出力）**へフォールバックして稼働します。
