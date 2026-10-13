package nodeui

import (
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

// startNode 建立單節點（非 follower，實際出塊）供 UI e2e 使用。
func startNode(t *testing.T) *node.Node {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.BlockTime = 1
	n, err := node.New(cfg, "ui-test-node", 1)
	if err != nil {
		t.Fatal(err)
	}
	// 創世塊（與 cmd/web、cmd/nodeweb 一致）：鏈庫為空時手動插入。
	g := &chaindb.Block{
		Height: 0, Hash: strings.Repeat("0", 64),
		Proposer: "genesis", Ts: time.Now().Unix(),
	}
	if err := n.DB().InsertBlock(g, nil); err != nil {
		t.Fatalf("創世塊寫入失敗: %v", err)
	}
	n.Start()
	t.Cleanup(func() { _ = n.Close() })
	return n
}

func getJSON(t *testing.T, srv *httptest.Server, path string, out any) {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s status=%d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatal(err)
	}
}

// TestNodeUIHome 首頁可訪問且為節點品牌內容（不含主網功能）。
func TestNodeUIHome(t *testing.T) {
	n := startNode(t)
	srv := httptest.NewServer(New(n, "http://anchor.test:8080").Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); cc == "" {
		t.Fatal("HTML 必須永不緩存（Cache-Control 缺失）")
	}
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(bodyBytes)
	for _, want := range []string{"TAC Ai Smart Chain", "Validator Node", "data-i18n", "nodeui.js"} {
		if !contains(body, want) {
			t.Fatalf("首頁缺少 %q", want)
		}
	}
	if contains(body, "交易所") || contains(body, "Wallet") {
		t.Fatal("節點首頁不應包含主網用戶功能字樣")
	}
}

// TestNodeUIStatusAPI 狀態 API 回傳鏈頂/地址/同步等節點資訊。
func TestNodeUIStatusAPI(t *testing.T) {
	n := startNode(t)
	srv := httptest.NewServer(New(n, "").Handler())
	defer srv.Close()

	// 等至少出 2 塊。
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if n.GetStatus().BlockHeight >= 2 {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}

	var st statusPayload
	getJSON(t, srv, "/api/status", &st)
	if st.BlockHeight < 2 {
		t.Fatalf("鏈頂異常: %d", st.BlockHeight)
	}
	if st.Address == "" || st.NodeID == "" {
		t.Fatal("節點地址/ID 缺失")
	}
	if st.MainnetURL != "http://anchor.test:8080" {
		// New("") 應套用預設錨點。
		if st.MainnetURL != "http://2.28.201.174:8080" {
			t.Fatalf("主網 URL 異常: %s", st.MainnetURL)
		}
	}
	if st.BehindBlocks < 0 || st.Uptime == "" {
		t.Fatal("同步/運行時間欄位異常")
	}
}

// TestNodeUIValidatorsAPI 驗證人 API 回傳創世驗證人集與權重。
func TestNodeUIValidatorsAPI(t *testing.T) {
	n := startNode(t)
	srv := httptest.NewServer(New(n, "http://x").Handler())
	defer srv.Close()

	var v struct {
		Validators []struct {
			Address string `json:"address"`
			Power   int    `json:"power"`
		} `json:"validators"`
		Total int `json:"total"`
		Power int `json:"power"`
	}
	getJSON(t, srv, "/api/validators", &v)
	if v.Total < 1 || v.Power < 1 {
		t.Fatalf("驗證人集異常: total=%d power=%d", v.Total, v.Power)
	}
	if v.Validators[0].Address == "" {
		t.Fatal("驗證人地址缺失")
	}
}

// TestNodeUIBlocksAPI 區塊 API 回傳有序區塊列表。
func TestNodeUIBlocksAPI(t *testing.T) {
	n := startNode(t)
	srv := httptest.NewServer(New(n, "http://x").Handler())
	defer srv.Close()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if n.GetStatus().BlockHeight >= 3 {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}

	var out struct {
		Blocks []map[string]any `json:"blocks"`
		Tip    int64            `json:"tip"`
	}
	getJSON(t, srv, "/api/blocks", &out)
	if out.Tip < 3 {
		t.Fatalf("鏈頂異常: %d", out.Tip)
	}
	if len(out.Blocks) < 3 {
		t.Fatalf("區塊數不足: %d", len(out.Blocks))
	}
	prev := int64(-1)
	for _, b := range out.Blocks {
		h := int64(b["height"].(float64))
		if h != prev+1 {
			t.Fatalf("區塊未依序: %d 前一個 %d", h, prev)
		}
		if b["hash"] == "" || b["proposer"] == "" {
			t.Fatal("區塊欄位缺失")
		}
		prev = h
	}
}

// TestNodeUIStatic 靜態資源（CSS/JS/SVG）可訪問。
func TestNodeUIStatic(t *testing.T) {
	n := startNode(t)
	srv := httptest.NewServer(New(n, "http://x").Handler())
	defer srv.Close()
	for _, p := range []string{"/static/node.css", "/static/nodeui.js", "/static/tacnode.svg"} {
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status=%d", p, resp.StatusCode)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
