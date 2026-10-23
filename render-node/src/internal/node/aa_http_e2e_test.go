package node

// M154：AA HTTP 層錯誤路徑測試（不重複 aa_rpc_test.go 已涵蓋的 happy-path/壞簽名/估算）。
// 涵蓋：壞 JSON → 400；缺 sender → 400；aaSvc 未初始化 → 503。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tacm/internal/aa"
)

// newAALightRPC 建一個最輕的 AA RPC server（withSvc=false 時故意不掛 aaSvc，驗 503 路徑）。
func newAALightRPC(t *testing.T, withSvc bool) *httptest.Server {
	t.Helper()
	n, err := New(testCfg(t), "aaerr", 3)
	if err != nil {
		t.Fatalf("建節點失敗: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	insertGenesis(t, n)
	n.Start()
	if withSvc {
		nodeAddr, _ := n.keypair.Address()
		n.aaSvc = aa.New(nodeAddr)
	} else {
		// 節點啟動時會自動掛 aaSvc；此處刻意清掉以驗證 handler 的 nil→503 路徑。
		n.aaSvc = nil
	}
	srv := httptest.NewServer(NewRPCServer(n).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func TestAAHTTPBadJSON(t *testing.T) {
	srv := newAALightRPC(t, true)

	// /aa/submit 壞 JSON → 400。
	resp, err := srv.Client().Post(srv.URL+"/aa/submit", "application/json", strings.NewReader("{ not json"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("壞 JSON POST /aa/submit 應 400，got %d", resp.StatusCode)
	}

	// /aa/estimate 壞 JSON → 400。
	resp2, err := srv.Client().Post(srv.URL+"/aa/estimate", "application/json", strings.NewReader("{ not json"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("壞 JSON POST /aa/estimate 應 400，got %d", resp2.StatusCode)
	}
}

func TestAAHTTPStatusMissingSender(t *testing.T) {
	srv := newAALightRPC(t, true)
	// 缺 sender → 400。
	resp, err := srv.Client().Get(srv.URL + "/aa/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("缺 sender GET /aa/status 應 400，got %d", resp.StatusCode)
	}
}

func TestAAHTTPServiceUnavailable(t *testing.T) {
	srv := newAALightRPC(t, false) // 不掛 aaSvc

	post := func(path string) int {
		r, err := srv.Client().Post(srv.URL+path, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		return r.StatusCode
	}
	if code := post("/aa/submit"); code != http.StatusServiceUnavailable {
		t.Errorf("aaSvc=nil POST /aa/submit 應 503，got %d", code)
	}
	if code := post("/aa/estimate"); code != http.StatusServiceUnavailable {
		t.Errorf("aaSvc=nil POST /aa/estimate 應 503，got %d", code)
	}
	// GET /aa/status 缺服務 → 503。
	r, err := srv.Client().Get(srv.URL + "/aa/status?sender=tx0-x")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("aaSvc=nil GET /aa/status 應 503，got %d", r.StatusCode)
	}
}
