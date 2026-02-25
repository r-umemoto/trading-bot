// internal/config/config.go
package config

import (
	"trading-bot/pkg/infra/kabu"

	"github.com/joho/godotenv"
	"github.com/kelseyhightower/envconfig"
)

// AppConfig はシステム全体の設定です
type AppConfig struct {
	BrokerType string      `envconfig:"BROKER_TYPE" default:"kabu"`
	Kabu       kabu.Config // ネストされた構造体も、タグに従って自動で読み込まれます
}

// Load は環境変数から設定を自動でマッピングして返します
func Load() (*AppConfig, error) {
	// 1. .envファイルがあれば読み込み、OSの環境変数にセットする
	// ※ 本番環境など .env が存在しない場合もあるため、エラーは無視（_）するのがベストプラクティスです
	_ = godotenv.Load()

	var cfg AppConfig

	if err := envconfig.Process("", &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}
