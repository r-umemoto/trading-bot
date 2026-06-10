package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/r-umemoto/trading-bot/pkg/infra/kabu/api"
)

func TestKabuClient_REST_All(t *testing.T) {
	// Setup mock API server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// 1. Check common headers
		if r.URL.Path != "/token" {
			if key := r.Header.Get("X-API-KEY"); key != "dummy-token" {
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"Code":    401,
					"Message": "Unauthorized: missing or invalid token",
				})
				return
			}
		}

		switch r.Method {
		case "POST":
			switch r.URL.Path {
			case "/token":
				var req api.TokenRequest
				_ = json.NewDecoder(r.Body).Decode(&req)
				if req.APIPassword == "wrong-pass" {
					w.WriteHeader(http.StatusBadRequest)
					json.NewEncoder(w).Encode(api.TokenResponse{
						ResultCode: -1,
					})
					return
				}
				json.NewEncoder(w).Encode(api.TokenResponse{
					ResultCode: 0,
					Token:      "dummy-token",
				})

			case "/sendorder":
				var req api.OrderRequest
				_ = json.NewDecoder(r.Body).Decode(&req)
				if req.Symbol == "invalid" {
					w.WriteHeader(http.StatusBadRequest)
					json.NewEncoder(w).Encode(map[string]interface{}{
						"Code":    8,
						"Message": "Invalid symbol code",
					})
					return
				}
				json.NewEncoder(w).Encode(api.OrderResponse{
					Result:  0,
					OrderId: "order-id-1234",
				})

			default:
				w.WriteHeader(http.StatusNotFound)
			}

		case "PUT":
			switch r.URL.Path {
			case "/cancelorder":
				var req api.CancelRequest
				_ = json.NewDecoder(r.Body).Decode(&req)
				json.NewEncoder(w).Encode(api.CancelResponse{
					Result:  0,
					OrderID: req.OrderID,
				})

			case "/register":
				var req api.RegisterSymbolRequest
				_ = json.NewDecoder(r.Body).Decode(&req)
				items := []api.RegistListItem{}
				for _, s := range req.Symbols {
					items = append(items, api.RegistListItem{Symbol: s.Symbol, Exchange: s.Exchange})
				}
				json.NewEncoder(w).Encode(api.RegisterSymbolResponse{
					RegistList: items,
				})

			case "/unregister/all":
				json.NewEncoder(w).Encode(api.UnregisterSymbolAllResponse{
					RegistList: []api.RegistListItem{},
				})
			default:
				w.WriteHeader(http.StatusNotFound)
			}

		case "GET":
			if r.URL.Path == "/orders" {
				json.NewEncoder(w).Encode([]api.Order{
					{ID: "order-1", Symbol: "7203", OrderQty: 100},
				})
			} else if r.URL.Path == "/positions" {
				prod := r.URL.Query().Get("product")
				if prod != "2" {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				json.NewEncoder(w).Encode([]api.Position{
					{ExecutionID: "exec-1", Symbol: "7203", LeavesQty: 100},
				})
			} else if r.URL.Path == "/board/7203@1" {
				json.NewEncoder(w).Encode(api.BoardResponse{
					Symbol:     "7203",
					SymbolName: "Toyota",
				})
			} else if r.URL.Path == "/symbol/7203@1" {
				json.NewEncoder(w).Encode(api.SymbolSuccess{
					Symbol:          "7203",
					SymbolName:      "Toyota",
					PriceRangeGroup: "1",
				})
			} else {
				w.WriteHeader(http.StatusNotFound)
			}

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	// Initialize client
	cfg := api.Config{
		APIURL:    server.URL,
		Password:  "correct-pass",
	}
	client := api.NewKabuClient(cfg)

	// 1. GetToken Success
	err := client.GetToken()
	if err != nil {
		t.Fatalf("GetToken failed: %v", err)
	}
	if client.Token != "dummy-token" {
		t.Errorf("expected Token dummy-token, got %s", client.Token)
	}

	// 2. GetToken Failure
	clientErr := api.NewKabuClient(api.Config{
		APIURL:    server.URL,
		Password:  "wrong-pass",
	})
	err = clientErr.GetToken()
	if err == nil {
		t.Fatal("expected GetToken to fail with incorrect password")
	}

	// 3. GetSymbol
	sym, err := client.GetSymbol("7203", api.EXCHANGE_TYPE_TOSHO)
	if err != nil {
		t.Fatalf("GetSymbol failed: %v", err)
	}
	if sym.SymbolName != "Toyota" {
		t.Errorf("expected Toyota, got %s", sym.SymbolName)
	}

	// 4. RegisterSymbol
	regResp, err := client.RegisterSymbol(api.RegisterSymbolRequest{
		Symbols: []api.RegisterSymbolsItem{
			{Symbol: "7203", Exchange: api.EXCHANGE_TYPE_TOSHO},
		},
	})
	if err != nil {
		t.Fatalf("RegisterSymbol failed: %v", err)
	}
	if len(regResp.RegistList) != 1 || regResp.RegistList[0].Symbol != "7203" {
		t.Errorf("unexpected register response: %+v", regResp)
	}

	// 5. UnregisterSymbolAll
	unregResp, err := client.UnregisterSymbolAll()
	if err != nil {
		t.Fatalf("UnregisterSymbolAll failed: %v", err)
	}
	if len(unregResp.RegistList) != 0 {
		t.Errorf("expected empty register list, got %+v", unregResp.RegistList)
	}

	// 6. GetBoard
	board, err := client.GetBoard("7203")
	if err != nil {
		t.Fatalf("GetBoard failed: %v", err)
	}
	if board.SymbolName != "Toyota" {
		t.Errorf("expected Toyota, got %s", board.SymbolName)
	}

	// 7. SendOrder Success
	ordResp, err := client.SendOrder(api.OrderRequest{Symbol: "7203"})
	if err != nil {
		t.Fatalf("SendOrder failed: %v", err)
	}
	if ordResp.OrderId != "order-id-1234" {
		t.Errorf("expected order-id-1234, got %s", ordResp.OrderId)
	}

	// 8. SendOrder Failure (API Error validation)
	_, err = client.SendOrder(api.OrderRequest{Symbol: "invalid"})
	if err == nil {
		t.Fatal("expected SendOrder to fail for invalid symbol")
	}
	var apiErr *api.KabuAPIError
	if ok := errAs(err, &apiErr); !ok {
		t.Fatalf("expected KabuAPIError, got %T", err)
	}
	if apiErr.Code != 8 {
		t.Errorf("expected error code 8, got %d", apiErr.Code)
	}
	if !apiErr.IsClientError() {
		t.Error("expected Code 8 to be recognized as a client-side error")
	}
	if apiErr.Error() == "" {
		t.Error("expected APIError.Error() to be non-empty")
	}

	// 9. CancelOrder
	canResp, err := client.CancelOrder(api.CancelRequest{OrderID: "order-id-1234"})
	if err != nil {
		t.Fatalf("CancelOrder failed: %v", err)
	}
	if canResp.OrderID != "order-id-1234" {
		t.Errorf("expected order-id-1234, got %s", canResp.OrderID)
	}

	// 10. GetOrders
	orders, err := client.GetOrders()
	if err != nil {
		t.Fatalf("GetOrders failed: %v", err)
	}
	if len(orders) != 1 || orders[0].ID != "order-1" {
		t.Errorf("unexpected orders response: %+v", orders)
	}

	// 11. GetPositions
	positions, err := client.GetPositions(api.ProductMargin)
	if err != nil {
		t.Fatalf("GetPositions failed: %v", err)
	}
	if len(positions) != 1 || positions[0].ExecutionID != "exec-1" {
		t.Errorf("unexpected positions response: %+v", positions)
	}
}

// Helper function to check error type
func errAs(err error, target interface{}) bool {
	type wrapper interface {
		Unwrap() error
	}
	for err != nil {
		if val, ok := err.(interface{ As(interface{}) bool }); ok && val.As(target) {
			return true
		}
		// Directly check type assertion
		if pTarget, ok := target.(**api.KabuAPIError); ok {
			if actual, ok := err.(*api.KabuAPIError); ok {
				*pTarget = actual
				return true
			}
		}
		// Unwrap
		if wrap, ok := err.(wrapper); ok {
			err = wrap.Unwrap()
		} else {
			break
		}
	}
	return false
}
