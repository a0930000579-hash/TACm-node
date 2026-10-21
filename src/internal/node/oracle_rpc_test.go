package node

// M152 Oracle RPC e2e 測試：喂價快照/單價/手動覆寫/歷史。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tacm/internal/oracle"
)

func newOracleTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	n, err := New(testCfg(t), "orc1", 3)
	if err != nil {
		t.Fatalf("建節點失敗: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	n.oracleSvc = oracle.New() // 測試節點未 Start，手動掛載喂價服務
	insertGenesisWithAlloc(t, n, testAlice)
	return httptest.NewServer(NewRPCServer(n).Handler())
}

func TestOracleRPCPrices(t *testing.T) {
	srv := newOracleTestServer(t)
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/oracle/prices")
	if err != nil {
		t.Fatalf("GET /oracle/prices 失敗: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("狀態碼應 200，got %d", resp.StatusCode)
	}
	var out struct {
		Ok     bool `json:"ok"`
		Prices []struct {
			Symbol string  `json:"symbol"`
			Price  float64 `json:"price"`
		} `json:"prices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("解析失敗: %v", err)
	}
	if !out.Ok {
		t.Fatal("ok 應為 true")
	}
	if len(out.Prices) < 1 {
		t.Fatal("prices 應至少含主流幣行情")
	}
	found := false
	for _, p := range out.Prices {
		if strings.EqualFold(p.Symbol, "BTC/USDT") && p.Price > 0 {
			found = true
		}
	}
	if !found {
		t.Fatal("快照應含 BTC/USDT 且價格 > 0")
	}
}

func TestOracleRPCFeedGetHistory(t *testing.T) {
	srv := newOracleTestServer(t)
	defer srv.Close()

	// 手動覆寫喂價。
	body := strings.NewReader(`{"symbol":"TACM/USDT","price":1.23}`)
	resp, err := srv.Client().Post(srv.URL+"/oracle/feed", "application/json", body)
	if err != nil {
		t.Fatalf("POST /oracle/feed 失敗: %v", err)
	}
	var fed struct {
		Ok bool   `json:"ok"`
		Fe string `json:"fed"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&fed)
	resp.Body.Close()
	if !fed.Ok || !strings.EqualFold(fed.Fe, "TACM/USDT") {
		t.Fatalf("喂價應成功，got %+v", fed)
	}

	// 查詢單價。
	q, err := srv.Client().Get(srv.URL + "/oracle/price?symbol=TACM/USDT")
	if err != nil {
		t.Fatalf("GET /oracle/price 失敗: %v", err)
	}
	var pr struct {
		Ok    bool `json:"ok"`
		Price struct {
			Symbol string  `json:"symbol"`
			Price  float64 `json:"price"`
			Source string  `json:"source"`
		} `json:"price"`
	}
	_ = json.NewDecoder(q.Body).Decode(&pr)
	q.Body.Close()
	if !pr.Ok || pr.Price.Price != 1.23 || pr.Price.Source != "manual" {
		t.Fatalf("單價查詢異常: %+v", pr)
	}

	// 歷史。
	h, err := srv.Client().Get(srv.URL + "/oracle/history?symbol=TACM/USDT")
	if err != nil {
		t.Fatalf("GET /oracle/history 失敗: %v", err)
	}
	var hist struct {
		Ok      bool `json:"ok"`
		History []struct {
			Price float64 `json:"price"`
		} `json:"history"`
	}
	_ = json.NewDecoder(h.Body).Decode(&hist)
	h.Body.Close()
	if !hist.Ok || len(hist.History) < 1 {
		t.Fatalf("歷史應含喂價紀錄，got %+v", hist)
	}

	// 非法喂價應 400。
	bad := strings.NewReader(`{"symbol":"X/USDT","price":-1}`)
	br, err := srv.Client().Post(srv.URL+"/oracle/feed", "application/json", bad)
	if err != nil {
		t.Fatalf("非法喂價請求失敗: %v", err)
	}
	br.Body.Close()
	if br.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法喂價應 400，got %d", br.StatusCode)
	}
}
