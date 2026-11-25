// internal/infrastructure/storage/csv_logger.go
package storage

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/r-umemoto/trading-bot/pkg/domain/tick"
)

// CSVLogger はTickデータを非同期でCSVに書き込むための構造体です
type CSVLogger struct {
	tickChan  chan tick.Tick
	done      chan struct{}
	mu        sync.RWMutex
	closed    bool
	closeOnce sync.Once
	file      *os.File
	writer    *csv.Writer
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
			"OpeningPrice", "PreviousClose", "TradingValue",
			"MarketOrderSellQty", "MarketOrderBuyQty",
			"OverSellQty", "UnderBuyQty",
		)
		writer.Write(header)
		writer.Flush()
	}

	logger := &CSVLogger{
		tickChan: make(chan tick.Tick, 10000), // バッファサイズ1万（急激なTick流入に備える）
		done:     make(chan struct{}),
		file:     file,
		writer:   writer,
	}

	// バックグラウンドで書き込み処理を開始
	go logger.startWriting()

	return logger, nil
}

// Log はTickデータをチャネルに送信します（Close 済みの場合は安全に無視します）
func (l *CSVLogger) Log(t tick.Tick) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.closed {
		return
	}
	l.tickChan <- t
}

// startWriting はチャネルからデータを受け取り、バッファリングしつつ定期的にフラッシュしてファイルへ書き込みます
func (l *CSVLogger) startWriting() {
	defer close(l.done)
	flushTicker := time.NewTicker(1 * time.Second)
	defer flushTicker.Stop()
	dirty := false

	writeRecord := func(t tick.Tick) {
		record := []string{
			t.CurrentPriceTime.Format("15:04:05.000"), // ミリ秒まで記録
			t.Symbol,
			strconv.FormatFloat(t.Price, 'f', -1, 64),
			strconv.FormatFloat(t.TradingVolume, 'f', -1, 64),
			strconv.FormatFloat(t.VWAP, 'f', -1, 64),
			strconv.FormatFloat(t.BestAsk.Price, 'f', -1, 64),
			strconv.FormatFloat(t.BestAsk.Qty, 'f', -1, 64),
			strconv.FormatFloat(t.BestBid.Price, 'f', -1, 64),
			strconv.FormatFloat(t.BestBid.Qty, 'f', -1, 64),
		}

		// 板10本の記録（2〜10本目）
		for i := 0; i < 9; i++ {
			askP, askQ, bidP, bidQ := 0.0, 0.0, 0.0, 0.0
			if i < len(t.SellBoard) {
				askP, askQ = t.SellBoard[i].Price, t.SellBoard[i].Qty
			}
			if i < len(t.BuyBoard) {
				bidP, bidQ = t.BuyBoard[i].Price, t.BuyBoard[i].Qty
			}
			record = append(record, strconv.FormatFloat(askP, 'f', -1, 64), strconv.FormatFloat(askQ, 'f', -1, 64))
			record = append(record, strconv.FormatFloat(bidP, 'f', -1, 64), strconv.FormatFloat(bidQ, 'f', -1, 64))
		}

		record = append(record,
			strconv.Itoa(int(t.CurrentPriceStatus)),
			string(t.CurrentPriceChangeStatus),
			strconv.FormatFloat(t.OpeningPrice, 'f', -1, 64),
			strconv.FormatFloat(t.PreviousClose, 'f', -1, 64),
			strconv.FormatFloat(t.TradingValue, 'f', -1, 64),
			strconv.FormatFloat(t.MarketOrderSellQty, 'f', -1, 64),
			strconv.FormatFloat(t.MarketOrderBuyQty, 'f', -1, 64),
			strconv.FormatFloat(t.OverSellQty, 'f', -1, 64),
			strconv.FormatFloat(t.UnderBuyQty, 'f', -1, 64),
		)

		_ = l.writer.Write(record)
		dirty = true
	}

	for {
		select {
		case t, ok := <-l.tickChan:
			if !ok {
				if dirty {
					l.writer.Flush()
				}
				return
			}
			writeRecord(t)
		case <-flushTicker.C:
			if dirty {
				l.writer.Flush()
				dirty = false
			}
		}
	}
}

// Close はキューに残ったTickをすべて書き終えてからファイルを安全に閉じます
func (l *CSVLogger) Close() {
	l.closeOnce.Do(func() {
		l.mu.Lock()
		l.closed = true
		close(l.tickChan)
		l.mu.Unlock()
		<-l.done
		l.writer.Flush()
		_ = l.file.Close()
	})
}
