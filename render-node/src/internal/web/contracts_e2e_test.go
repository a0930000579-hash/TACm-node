package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tacm/internal/chaindb"
	"tacm/internal/config"
	"tacm/internal/node"
)

// M139：合約驗證中心頁面與 ABI read 互動端到端驗證。
//  1. GET /contracts 頁面渲染（Contract Verifier + i18n 鍵）
//  2. 經 web mux（同埠模式）POST /contract/deploy 部署代幣
//  3. POST /contract/read totalSupply/name 走 ABI 編碼回正確值
func TestContractVerifierPage(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.BlockTime = 1

	n, err := node.New(cfg, "ctrle2e", 1)
	if err != nil {
		t.Fatalf("New node: %v", err)
	}
	defer n.Close()

	now := time.Now().Unix()
	genesis := &chaindb.Block{
		Height: 0, Hash: strings.Repeat("0", 64),
		Proposer: "genesis", Ts: now,
	}
	if err := n.DB().InsertBlock(genesis, nil); err != nil {
		t.Fatalf("創世: %v", err)
	}
	n.Start()

	srv, err := New(NewNodeDataSource(n))
	if err != nil {
		t.Fatalf("New web: %v", err)
	}
	wmux, ok := srv.Handler().(*http.ServeMux)
	if !ok {
		t.Fatalf("web handler 非 ServeMux: %T", srv.Handler())
	}
	node.NewRPCServer(n).MountInto(wmux)
	ts := httptest.NewServer(wmux)
	defer ts.Close()

	// 1. /contracts 頁面渲染。
	resp, err := http.Get(ts.URL + "/contracts")
	if err != nil {
		t.Fatalf("GET /contracts: %v", err)
	}
	body := readAllString(resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/contracts 狀態碼 %d", resp.StatusCode)
	}
	for _, want := range []string{"Contract Verifier", "totalSupply()", "data-i18n=\"contract.title\""} {
		if !strings.Contains(body, want) {
			t.Fatalf("/contracts 缺 %q", want)
		}
	}

	// 2. 部署代幣（走 web mux 的 /contract/deploy）。
	deployBody := map[string]any{"name": "VerifierToken", "symbol": "VRT", "supply": "12345"}
	raw, _ := json.Marshal(deployBody)
	dresp, err := http.Post(ts.URL+"/contract/deploy", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST /contract/deploy: %v", err)
	}
	var dj map[string]any
	if err := json.NewDecoder(dresp.Body).Decode(&dj); err != nil {
		t.Fatalf("解析 deploy: %v", err)
	}
	dresp.Body.Close()
	if dj["ok"] != true {
		t.Fatalf("部署失敗: %v", dj)
	}
	addr := dj["contract_address"].(string)

	// 等入塊（合約 code 寫入）。
	deadline := time.Now().Add(8 * time.Second)
	for {
		info, err := http.Get(ts.URL + "/contract/get/" + addr)
		if err == nil {
			var gi map[string]any
			if json.NewDecoder(info.Body).Decode(&gi) == nil && gi["code_size"] != nil && gi["code_size"].(float64) > 0 {
				info.Body.Close()
				break
			}
			info.Body.Close()
		}
		if time.Now().After(deadline) {
			t.Fatal("合約未在時限內入塊")
		}
		time.Sleep(200 * time.Millisecond)
	}

	// 3. read：totalSupply() → 12345。
	r1 := contractRead(t, ts, map[string]any{
		"address": addr, "method_sig": "totalSupply()",
		"inputs": []string{}, "args": []any{}, "outputs": []string{"uint256"},
	})
	if r1["ok"] != true {
		t.Fatalf("totalSupply read 失敗: %v", r1)
	}
	outs, _ := r1["outputs"].([]any)
	if len(outs) != 1 || outs[0] != "12345" {
		t.Fatalf("totalSupply 解碼不符: %v", outs)
	}

	// 4. read：name() → VerifierToken（本鏈簡化慣例 fallback）。
	r2 := contractRead(t, ts, map[string]any{
		"address": addr, "method_sig": "name()",
		"inputs": []string{}, "args": []any{}, "outputs": []string{"string"},
	})
	if r2["ok"] != true {
		t.Fatalf("name read 失敗: %v", r2)
	}
	outs2, _ := r2["outputs"].([]any)
	if len(outs2) != 1 || outs2[0] != "VerifierToken" {
		t.Fatalf("name 解碼不符: %v", outs2)
	}

	// 5. verify ABI → abi 回讀 → 頁面含 Load ABI 面板。
	abi := `[{"type":"function","name":"totalSupply","inputs":[],"outputs":[{"type":"uint256"}]},{"type":"function","name":"name","inputs":[],"outputs":[{"type":"string"}]}]`
	vBody := map[string]any{"address": addr, "name": "VerifierToken", "abi": abi}
	vraw, _ := json.Marshal(vBody)
	vresp, err := http.Post(ts.URL+"/contract/verify", "application/json", bytes.NewReader(vraw))
	if err != nil {
		t.Fatalf("POST /contract/verify: %v", err)
	}
	var vj map[string]any
	if err := json.NewDecoder(vresp.Body).Decode(&vj); err != nil {
		t.Fatalf("解析 verify: %v", err)
	}
	vresp.Body.Close()
	if vj["ok"] != true {
		t.Fatalf("verify 失敗: %v", vj)
	}
	aresp, err := http.Get(ts.URL + "/contract/abi/" + addr)
	if err != nil {
		t.Fatalf("GET /contract/abi: %v", err)
	}
	var aj map[string]any
	if err := json.NewDecoder(aresp.Body).Decode(&aj); err != nil {
		t.Fatalf("解析 abi: %v", err)
	}
	aresp.Body.Close()
	if aj["abi"] != abi {
		t.Fatalf("abi 回讀不符: %v", aj)
	}
	// 頁面含 Load ABI / Verify 面板元素。
	body2 := readAllString(mustGet(t, ts.URL+"/contracts"))
	for _, want := range []string{"Load ABI", "ctMethodSel", "vtGo", "data-i18n=\"contract.verifyTitle\""} {
		if !strings.Contains(body2, want) {
			t.Fatalf("/contracts 缺 %q", want)
		}
	}
}

func mustGet(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}

func contractRead(t *testing.T, ts *httptest.Server, body map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(ts.URL+"/contract/read", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST /contract/read: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("解析 read: %v", err)
	}
	return out
}
