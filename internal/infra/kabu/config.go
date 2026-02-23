// internal/infra/kabu/config.go （または client.go の上部）
package kabu

// Config はカブコムAPIを動かすために必要な設定です
type Config struct {
	// タグをつけるだけで、ライブラリが勝手に読み込んでくれます
	APIURL   string `envconfig:"KABU_API_URL" default:"http://localhost:18080/kabusapi"`
	Password string `envconfig:"KABU_PASSWORD" required:"true"`
}
