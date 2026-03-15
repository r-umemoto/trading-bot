// internal/infrastructure/feed/csv_feeder.go
package feed

import (
	"encoding/csv"
	"io"
	"os"
	"strconv"

	"github.com/r-umemoto/trading-bot/pkg/domain/market"
)

// CSVTickFeeder はCSVからTickデータを読み込み、システムに供給します
type CSVTickFeeder struct {
	filePath string
}

func NewCSVTickFeeder(filePath string) *CSVTickFeeder {
	return &CSVTickFeeder{
		filePath: filePath,
	}
}

// Run はCSVを末尾まで読み込み、tickChan にデータを送信し続けます
func (f *CSVTickFeeder) Run(tickChan chan<- market.Tick) error {
	file, err := os.Open(f.filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	reader := csv.NewReader(file)

	// ヘッダー行をスキップ
	if _, err := reader.Read(); err != nil {
		return err
	}

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break // ファイルの末尾に到達
		}
		if err != nil {
			return err
		}

		// CSVの各カラムを元の型にパース
		// フォーマット: "Time", "Symbol", "Price", "TradingVolume", "VWAP"
		//parsedTime, _ := time.Parse("15:04:05.000", record[0])
		price, _ := strconv.ParseFloat(record[2], 64)
		volume, _ := strconv.ParseFloat(record[3], 64)
		vwap, _ := strconv.ParseFloat(record[4], 64)

		tick := market.Tick{
			Symbol:        record[1],
			Price:         price,
			TradingVolume: volume,
			VWAP:          vwap,
		}

		// バックテストエンジン（またはAnalyzer）に向けてTickを送信
		tickChan <- tick
	}

	// 全データの送信が完了したらチャネルを閉じる
	close(tickChan)
	return nil
}
