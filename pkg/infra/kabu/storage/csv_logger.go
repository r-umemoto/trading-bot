// internal/infrastructure/storage/csv_logger.go
package storage

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
)

// CSVLogger はTickデータを非同期でCSVに書き込むための構造体です
type CSVLogger struct {
	tickChan chan market.Tick
	file     *os.File
	writer   *csv.Writer
}

// NewCSVLogger は指定した銘柄・日付のCSVファイルを作成し、ロガーを初期化します
func NewCSVLogger(symbol string, date string, outputDir string) (*CSVLogger, error) {
	// ディレクトリが存在しない場合は作成
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, err
	}

	// ファイル名は "7203_20260311.csv" のような形式
	filename := fmt.Sprintf("%s_%s.csv", symbol, date)
	filepath := filepath.Join(outputDir, filename)

	// ファイルを追記モードで開く（存在しない場合は作成）
	file, err := os.OpenFile(filepath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}

	writer := csv.NewWriter(file)

	// ファイルが空（新規作成）の場合はヘッダーを書き込む
	stat, _ := file.Stat()
	if stat.Size() == 0 {
		header := []string{
			"Time", "Symbol", "Price", "TradingVolume", "VWAP",
			"BestAskPrice", "BestAskQty", "BestBidPrice", "BestBidQty",
		}
		// 板10本分（2〜10本目）を追加
		for i := 2; i <= 10; i++ {
			header = append(header, fmt.Sprintf("Ask%dP", i), fmt.Sprintf("Ask%dQ", i))
			header = append(header, fmt.Sprintf("Bid%dP", i), fmt.Sprintf("Bid%dQ", i))
		}
		header = append(header,
			"CurrentPriceStatus", "CurrentPriceChangeStatus",
			"OpeningPrice", "TradingValue",
			"MarketOrderSellQty", "MarketOrderBuyQty",
			"OverSellQty", "UnderBuyQty",
		)
		writer.Write(header)
		writer.Flush()
	}

	logger := &CSVLogger{
		tickChan: make(chan market.Tick, 10000), // バッファサイズ1万（急激なTick流入に備える）
		file:     file,
		writer:   writer,
	}

	// バックグラウンドで書き込み処理を開始
	go logger.startWriting()

	return logger, nil
}

// Log はTickデータをチャネルに送信します（呼び出し元はブロックされません）
func (l *CSVLogger) Log(tick market.Tick) {
	// バッファが一杯の場合は破棄するか待つかの制御が必要ですが、
	// 10000のバッファがあれば通常のTick流量で詰まることはほぼありません
	l.tickChan <- tick
}

// startWriting はチャネルからデータを受け取り、継続的にファイルへ書き込みます
func (l *CSVLogger) startWriting() {
	for tick := range l.tickChan {
		record := []string{
			tick.CurrentPriceTime.Format("15:04:05.000"), // ミリ秒まで記録
			tick.Symbol,
			strconv.FormatFloat(tick.Price, 'f', -1, 64),
			strconv.FormatFloat(tick.TradingVolume, 'f', -1, 64),
			strconv.FormatFloat(tick.VWAP, 'f', -1, 64),
			strconv.FormatFloat(tick.BestAsk.Price, 'f', -1, 64),
			strconv.FormatFloat(tick.BestAsk.Qty, 'f', -1, 64),
			strconv.FormatFloat(tick.BestBid.Price, 'f', -1, 64),
			strconv.FormatFloat(tick.BestBid.Qty, 'f', -1, 64),
		}

		// 板10本の記録（2〜10本目）
		for i := 0; i < 9; i++ {
			askP, askQ, bidP, bidQ := 0.0, 0.0, 0.0, 0.0
			if i < len(tick.SellBoard) {
				askP, askQ = tick.SellBoard[i].Price, tick.SellBoard[i].Qty
			}
			if i < len(tick.BuyBoard) {
				bidP, bidQ = tick.BuyBoard[i].Price, tick.BuyBoard[i].Qty
			}
			record = append(record, strconv.FormatFloat(askP, 'f', -1, 64), strconv.FormatFloat(askQ, 'f', -1, 64))
			record = append(record, strconv.FormatFloat(bidP, 'f', -1, 64), strconv.FormatFloat(bidQ, 'f', -1, 64))
		}

		record = append(record,
			strconv.Itoa(int(tick.CurrentPriceStatus)),
			string(tick.CurrentPriceChangeStatus),
			strconv.FormatFloat(tick.OpeningPrice, 'f', -1, 64),
			strconv.FormatFloat(tick.TradingValue, 'f', -1, 64),
			strconv.FormatFloat(tick.MarketOrderSellQty, 'f', -1, 64),
			strconv.FormatFloat(tick.MarketOrderBuyQty, 'f', -1, 64),
			strconv.FormatFloat(tick.OverSellQty, 'f', -1, 64),
			strconv.FormatFloat(tick.UnderBuyQty, 'f', -1, 64),
		)

		l.writer.Write(record)
		l.writer.Flush() // Tickごとに確実にディスクへ保存
	}
}

// Close はチャネルとファイルを安全に閉じます
func (l *CSVLogger) Close() {
	close(l.tickChan)
	l.file.Close()
}
