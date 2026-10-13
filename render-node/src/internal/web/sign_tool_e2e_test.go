package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tacm/internal/chaindb"
	"tacm/internal/config"
	"tacm/internal/node"
)

// ethHTTPPost 對 /eth 發 JSON-RPC POST，回原始 body 字串。
func ethHTTPPost(base, method string, params []any) (string, error) {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return "", err
	}
	resp, err := http.Post(base+"/eth", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// extractHex 從 JSON-RPC result 字串抽出第一個 0x… 欄位值。
func extractHex(s string) string {
	idx := strings.Index(s, `"result":"0x`)
	if idx < 0 {
		return s
	}
	rest := s[idx+len(`"result":"0x`):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		end = len(rest)
	}
	return "0x" + rest[:end]
}

// M136-C：簽名工具台頁面渲染與 eth 簽名 RPC 端到端驗證。
func TestSignToolPage(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.BlockTime = 1

	n, err := node.New(cfg, "signe2e", 1)
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
	// 同埠模式：web mux 掛載 node RPC（與 cmd/web 一致，含 /eth）。
	wmux, ok := srv.Handler().(*http.ServeMux)
	if !ok {
		t.Fatalf("web handler 非 ServeMux: %T", srv.Handler())
	}
	node.NewRPCServer(n).MountInto(wmux)
	ts := httptest.NewServer(wmux)
	defer ts.Close()

	// 1. /sign 頁面 200 且含關鍵元素。
	resp, err := http.Get(ts.URL + "/sign")
	if err != nil {
		t.Fatalf("GET /sign: %v", err)
	}
	body := readAllString(resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/sign 狀態=%d", resp.StatusCode)
	}
	for _, want := range []string{"Signature Tool", "personal_sign", "eth_signTypedData_v4", "personal_ecRecover", "eth_ecRecover"} {
		if !strings.Contains(body, want) {
			t.Errorf("/sign 缺少 %q", want)
		}
	}

	// 2. /sign 頁面經 /eth 端點完成簽名→恢復閉環（模擬前端呼叫）。
	nodeAddr := ""
	{
		body, err := ethHTTPPost(ts.URL, "eth_accounts", []any{})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(body, `"result"`) {
			t.Fatalf("eth_accounts 異常: %s", body)
		}
		// 提取 0x 地址。
		idx := strings.Index(body, `"0x`)
		if idx < 0 {
			t.Fatalf("eth_accounts 無 0x 地址: %s", body)
		}
		nodeAddr = body[idx+1 : idx+43]
	}

	msg := "0x68656c6c6f"
	sig, err := ethHTTPPost(ts.URL, "personal_sign", []any{nodeAddr, msg})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := ethHTTPPost(ts.URL, "personal_ecRecover", []any{msg, extractHex(sig)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec, nodeAddr) {
		t.Fatalf("簽名恢復不符: %s vs %s", rec, nodeAddr)
	}
}
