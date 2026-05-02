// internal/usecase/trade_usecase.go
package usecase

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
	"github.com/r-umemoto/trading-bot/pkg/domain/sniper"
)

// TradeUseCase は価格更新イベントを受け取り、該当するスナイパーに伝達するユースケースです
type TradeUseCase struct {
	snipers       []*sniper.Sniper
	gateway       market.MarketGateway
	dataPool      market.DataPool
	tickChannels  map[string]chan market.Tick         // 銘柄ごとのTick処理チャネル
	orderChannels map[string]chan market.OrdersReport // 銘柄ごとの注文レポートチャネル
}

func NewTradeUseCase(snipers []*sniper.Sniper, gateway market.MarketGateway, dataPool market.DataPool) *TradeUseCase {
	uc := &TradeUseCase{
		snipers:       snipers,
		gateway:       gateway,
		dataPool:      dataPool,
		tickChannels:  make(map[string]chan market.Tick),
		orderChannels: make(map[string]chan market.OrdersReport),
	}

	// 銘柄ごとにチャネルを作成
	for _, s := range snipers {
		if _, exists := uc.tickChannels[s.Detail.Code]; !exists {
			// バッファサイズは適宜調整（ここでは100）
			uc.tickChannels[s.Detail.Code] = make(chan market.Tick, 100)
			uc.orderChannels[s.Detail.Code] = make(chan market.OrdersReport, 100)
		}
	}

	return uc
}

// StartWorkers は銘柄ごとのワーカー（Goroutine）を起動します
// Engineの起動時（Run）などに呼ばれることを想定しています
func (u *TradeUseCase) StartWorkers(ctx context.Context) {
	for symbol := range u.tickChannels {
		go u.worker(ctx, symbol, u.tickChannels[symbol], u.orderChannels[symbol])
	}
}

// worker は特定の銘柄のTickや約定通知を専用に処理するGoroutineです
func (u *TradeUseCase) worker(ctx context.Context, symbol string, tickCh <-chan market.Tick, orderCh <-chan market.OrdersReport) {
	for {
		select {
		case <-ctx.Done():
			return
		case tick := <-tickCh:
			// この銘柄を担当するスナイパーを探してTick処理を実行
			u.processTickForSymbol(ctx, tick, symbol)
		case report := <-orderCh:
			// この銘柄を担当するスナイパーを探して注文同期を実行
			u.processOrdersReportForSymbol(report, symbol)
		}
	}
}

func (u *TradeUseCase) processTickForSymbol(ctx context.Context, tick market.Tick, symbol string) {
	u.dataPool.PushTick(tick)

	for _, s := range u.snipers {
		if s.Detail.Code == symbol {
			// 1. スナイパーに考えさせる
			orderPtr, req, cancelOrderID := s.Tick(u.dataPool)

			// 2. キャンセル要求があれば実行
			if cancelOrderID != "" {
				fmt.Printf("🛑 自動キャンセルを実行します: %s\n", cancelOrderID)
				if err := u.gateway.CancelOrder(ctx, cancelOrderID); err != nil {
					fmt.Printf("❌ キャンセル失敗: %v\n", err)
				}
				continue
			}

			if req != nil {
				// 2. 要求があれば、市場（インフラ）に発注する
				orderID, err := u.gateway.SendOrder(ctx, *req)
				if err != nil {
					fmt.Printf("❌ 発注失敗: %v\n", err)
					s.FailSendingOrder(orderPtr) // 通信失敗時は仮注文をリストから消去
					continue
				}

				// 3. 発注が成功したら、スナイパー側の仮注文オブジェクトIDを正式なものに更新する
				s.ConfirmOrder(orderPtr, orderID)
				fmt.Printf("✅ 注文受付IDを記録しました: %s\n", orderID)
			}
		}
	}
}

// HandleTick は市場のTickデータを受け取り、該当銘柄のチャネルへルーティングします
func (u *TradeUseCase) HandleTick(ctx context.Context, tick market.Tick) {
	if ch, ok := u.tickChannels[tick.Symbol]; ok {
		select {
		case ch <- tick:
			// 正常にキューイング完了
		default:
			// チャネルが詰まっている場合（ワーカの処理が追いついていない）
			fmt.Printf("⚠️ 警告: %s のTickチャネルがフルです。Tickがスキップされるか遅延します。\n", tick.Symbol)
			// ブロックさせるか、破棄するかは要件次第（ここではブロックする）
			ch <- tick
		}
	}
}

// HandleExecution は、インフラ層から流れてきた注文レポートを該当銘柄のチャネルへルーティングします
func (u *TradeUseCase) HandleExecution(report market.OrdersReport) {
	// 全銘柄分を個別にルーティングするのは効率が悪いが、現状の構造に合わせる
	// 本来は OrdersReport が銘柄ごとに分割されているか、ここで分割して投げる
	for symbol, ch := range u.orderChannels {
		// report.Orders の中から該当銘柄のものだけを抽出するフィルタリングが必要だが、
		// Sniper.SyncOrders 側でも Symbol チェックしているので、一旦そのまま流す
		select {
		case ch <- report:
		default:
			fmt.Printf("⚠️ 警告: %s のOrdersReportチャネルがフルです。処理がスキップされるか遅延します。\n", symbol)
		}
	}
}

func (u *TradeUseCase) processOrdersReportForSymbol(report market.OrdersReport, symbol string) {
	for _, s := range u.snipers {
		if s.Detail.Code == symbol {
			s.SyncOrders(report.Orders)
		}
	}
}

func (u *TradeUseCase) GetSnipers() []*sniper.Sniper {
	return u.snipers
}

func (u *TradeUseCase) GetDataPool() market.DataPool {
	return u.dataPool
}

type AggregatedPerformance struct {
	Name          string
	Trades        int
	Wins          int
	Losses        int
	RealizedPnL   float64
	UnrealizedPnL float64
}

// PrintPerformanceReport summarizes and prints the performance of all snipers.
func (u *TradeUseCase) PrintPerformanceReport(enableCSV bool) {

	// キー: "Symbol|StrategyName"
	perfMap := make(map[string]*AggregatedPerformance)
	symPerfMap := make(map[string]*AggregatedPerformance)
	stratPerfMap := make(map[string]*AggregatedPerformance)
	totalPerf := &AggregatedPerformance{Name: "Total"}

	for _, s := range u.snipers {
		stratName := s.Strategy.Name()
		key := s.Detail.Code + "|" + stratName

		if perfMap[key] == nil {
			perfMap[key] = &AggregatedPerformance{Name: strings.Replace(key, "|", " x ", 1)}
		}
		if symPerfMap[s.Detail.Code] == nil {
			symPerfMap[s.Detail.Code] = &AggregatedPerformance{Name: s.Detail.Code}
		}
		if stratPerfMap[stratName] == nil {
			stratPerfMap[stratName] = &AggregatedPerformance{Name: stratName}
		}

		// 含み損益の計算
		var unrealized float64
		marketState := u.dataPool.GetState(s.Detail.Code)
		if !marketState.LatestTick.CurrentPriceTime.IsZero() {
			latestPrice := marketState.LatestTick.Price
			unrealized = s.CalcUnrealizedPnL(latestPrice)
		}

		// 成績を集計
		updatePerf := func(p *AggregatedPerformance) {
			p.Trades += s.Performance.Trades
			p.Wins += s.Performance.Wins
			p.Losses += s.Performance.Losses
			p.RealizedPnL += s.Performance.RealizedPnL
			p.UnrealizedPnL += unrealized // 最新の含み損益を使用
		}

		updatePerf(perfMap[key])
		updatePerf(symPerfMap[s.Detail.Code])
		updatePerf(stratPerfMap[stratName])
		updatePerf(totalPerf)
	}

	printPerf := func(name string, p *AggregatedPerformance) {
		winRate := 0.0
		if p.Trades > 0 {
			winRate = float64(p.Wins) / float64(p.Trades) * 100
		}
		fmt.Printf("%-20s | 取引: %4d回 | 勝率: %5.1f%% (%4d勝 %4d敗) | 実現損益: %+10.0f 円 | 含み損益: %+10.0f 円 | 合計: %+10.0f 円\n",
			name, p.Trades, winRate, p.Wins, p.Losses, p.RealizedPnL, p.UnrealizedPnL, p.RealizedPnL+p.UnrealizedPnL)
	}

	fmt.Println("\n=============================================")
	fmt.Println("             トレード成績サマリー")
	fmt.Println("=============================================")

	fmt.Println("\n=== 1. 全体成績 (Total Performance) ===")
	printPerf("Total", totalPerf)

	fmt.Println("\n=== 2. 銘柄別成績 (Performance by Symbol) ===")
	for _, p := range symPerfMap {
		printPerf(p.Name, p)
	}

	fmt.Println("\n=== 3. ストラテジー別成績 (Performance by Strategy) ===")
	for _, p := range stratPerfMap {
		printPerf(p.Name, p)
	}

	fmt.Println("\n=== 4. 銘柄 × ストラテジー相性 (Performance by Symbol + Strategy) ===")
	for _, p := range perfMap {
		printPerf(p.Name, p)
	}
	fmt.Println("=============================================")

	if enableCSV {
		u.saveToCSV(totalPerf, symPerfMap, stratPerfMap, perfMap)
	}
}

func (u *TradeUseCase) saveToCSV(total *AggregatedPerformance, symbols map[string]*AggregatedPerformance, strats map[string]*AggregatedPerformance, combined map[string]*AggregatedPerformance) {
	outputDir := "data"
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		fmt.Printf("❌ ディレクトリ作成失敗: %v\n", err)
		return
	}

	now := time.Now().Format("20060102_150405")
	filename := fmt.Sprintf("performance_%s.csv", now)
	fullpath := filepath.Join(outputDir, filename)

	file, err := os.Create(fullpath)
	if err != nil {
		fmt.Printf("❌ CSV作成失敗: %v\n", err)
		return
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	// ヘッダー
	writer.Write([]string{"Type", "Name", "Trades", "Wins", "Losses", "WinRate", "RealizedPnL", "UnrealizedPnL", "TotalPnL"})

	writeLine := func(t, name string, p *AggregatedPerformance) {
		winRate := 0.0
		if p.Trades > 0 {
			winRate = float64(p.Wins) / float64(p.Trades) * 100
		}
		writer.Write([]string{
			t,
			name,
			strconv.Itoa(p.Trades),
			strconv.Itoa(p.Wins),
			strconv.Itoa(p.Losses),
			fmt.Sprintf("%.1f", winRate),
			fmt.Sprintf("%.0f", p.RealizedPnL),
			fmt.Sprintf("%.0f", p.UnrealizedPnL),
			fmt.Sprintf("%.0f", p.RealizedPnL+p.UnrealizedPnL),
		})
	}

	writeLine("Total", total.Name, total)
	for _, p := range symbols {
		writeLine("Symbol", p.Name, p)
	}
	for _, p := range strats {
		writeLine("Strategy", p.Name, p)
	}
	for _, p := range combined {
		writeLine("SymbolStrategy", p.Name, p)
	}

	fmt.Printf("💾 成績をCSVに保存しました: %s\n", fullpath)
}
