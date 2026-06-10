package engine_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/r-umemoto/trading-bot/pkg/config"
	"github.com/r-umemoto/trading-bot/pkg/domain/order"
	"github.com/r-umemoto/trading-bot/pkg/engine"
	"github.com/r-umemoto/trading-bot/pkg/infra/kabu/api"
	"github.com/r-umemoto/trading-bot/pkg/portfolio"
)

func TestBuildEngine(t *testing.T) {
	// Clean up logs directory created during deploySnipers
	defer os.RemoveAll("logs")

	// Set up a mock API server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			// Token response
			resp := api.TokenResponse{
				ResultCode: 0,
				Token:      "dummy-test-token",
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		case "/symbol/7203@1":
			// Symbol response for code 7203 (TOKYO Exchange = 1)
			resp := api.SymbolSuccess{
				Symbol:          "7203",
				SymbolName:      "Toyota Motor",
				PriceRangeGroup: "1", // Needs to be a valid integer string for strconv.Atoi
				UpperLimit:      3000,
				LowerLimit:      1500,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	// Prepare config pointing to our test server
	cfg := &config.AppConfig{
		BrokerType: "kabu",
		Kabu: api.Config{
			APIURL:    server.URL,
			Password:  "test-pass",
		},
	}

	targets := []portfolio.SymbolTarget{
		{
			Symbol:   "7203",
			Name:     "Toyota",
			Exchange: order.EXCHANGE_TOSHO,
			Enabled:  true,
		},
	}

	opTargets := []portfolio.OperationTarget{
		{
			Type: "default",
			ID:   "TestOp_7203",
			Params: map[string]interface{}{
				"symbol":     "7203",
				"strategies": []interface{}{"sample"},
			},
		},
	}

	ctx := context.Background()
	eng, err := engine.BuildEngine(ctx, cfg, targets, opTargets)
	if err != nil {
		t.Fatalf("BuildEngine failed: %v", err)
	}

	if eng == nil {
		t.Fatal("expected engine to be non-nil")
	}
}

func TestBuildEngine_UnsupportedBroker(t *testing.T) {
	cfg := &config.AppConfig{
		BrokerType: "unknown-broker",
	}

	ctx := context.Background()
	_, err := engine.BuildEngine(ctx, cfg, nil, nil)
	if err == nil {
		t.Fatal("expected BuildEngine to fail for unsupported broker")
	}
}
