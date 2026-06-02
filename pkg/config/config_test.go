package config_test

import (
	"os"
	"testing"

	"github.com/r-umemoto/trading-bot/pkg/config"
)

func TestLoad(t *testing.T) {
	t.Run("デフォルト値が正しく設定されること", func(t *testing.T) {
		// BROKER_TYPE が設定されている場合は一時的に未定義にする
		if orig, exists := os.LookupEnv("BROKER_TYPE"); exists {
			os.Unsetenv("BROKER_TYPE")
			t.Cleanup(func() {
				os.Setenv("BROKER_TYPE", orig)
			})
		}
		// 必須の環境変数をセット
		t.Setenv("KABU_PASSWORD", "dummy_pass")

		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("Load() で想定外のエラーが発生しました: %v", err)
		}

		if cfg.BrokerType != "kabu" {
			t.Errorf("期待値: 'kabu', 実際の値: '%s'", cfg.BrokerType)
		}
	})

	t.Run("環境変数が指定された場合に正しく上書きされること", func(t *testing.T) {
		t.Setenv("BROKER_TYPE", "mock")
		// 必須の環境変数をセット
		t.Setenv("KABU_PASSWORD", "dummy_pass")

		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("Load() で想定外のエラーが発生しました: %v", err)
		}

		if cfg.BrokerType != "mock" {
			t.Errorf("期待値: 'mock', 実際の値: '%s'", cfg.BrokerType)
		}
	})
}
