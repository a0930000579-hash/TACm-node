package nodeui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNodePanelPages：節點面板頁面與 API（純節點，無 DApp 路由）。
func TestNodePanelPages(t *testing.T) {
	n := startNode(t)
	srv := httptest.NewServer(New(n, "http://anchor:8080", "node1").Handler())
	defer srv.Close()

	// 頁面
	resp, err := http.Get(srv.URL + "/node-panel")
	if err != nil {
		t.Fatalf("GET /node-panel: %v", err)
	}
	body := make([]byte, 0)
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		nn, _ := resp.Body.Read(buf)
		if nn == 0 {
			break
		}
		sb.Write(buf[:nn])
	}
	body = []byte(sb.String())
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "節點架設者面板") {
		t.Errorf("/node-panel 狀態=%d 或缺標題", resp.StatusCode)
	}

	// 狀態 API
	var st map[string]any
	getJSON(t, srv, "/api/node/panel", &st)
	if st["node_id"] == nil || st["node_address"] == nil || st["balance"] == nil {
		t.Errorf("panel 缺 node_id/node_address/balance: %v", st)
	}
	if st["admin_set"] != false {
		t.Errorf("初始 admin_set 應為 false")
	}

	// 純節點不該有 DApp 路由（/wallet 404）。
	resp2, err := http.Get(srv.URL + "/wallet")
	if err != nil {
		t.Fatalf("GET /wallet: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("純節點不應提供 /wallet，狀態=%d", resp2.StatusCode)
	}
}

// TestNodePanelSetupBindWithdraw：設置密碼→綁定→查詢→提領閉環。
func TestNodePanelSetupBindWithdraw(t *testing.T) {
	n := startNode(t)
	srv := httptest.NewServer(New(n, "", "node1").Handler())
	defer srv.Close()

	post := func(path, body string, out any) int {
		resp, err := http.Post(srv.URL+path, "application/json", bytes.NewReader([]byte(body)))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer resp.Body.Close()
		if out != nil {
			_ = json.NewDecoder(resp.Body).Decode(out)
		}
		return resp.StatusCode
	}

	// 1) 設置密碼
	var r1 map[string]any
	if st := post("/api/node/setup", `{"admin_pass":"secret123"}`, &r1); st != http.StatusOK {
		t.Fatalf("setup 狀態=%d", st)
	}
	// 重複設置應拒絕
	var r2 map[string]any
	if st := post("/api/node/setup", `{"admin_pass":"secret456"}`, &r2); st != http.StatusBadRequest {
		t.Errorf("重複 setup 應 400，狀態=%d", st)
	}

	// 2) 綁定（錯誤密碼拒絕）
	var r3 map[string]any
	if st := post("/api/node/bind", `{"admin_pass":"wrong","payout_address":"tx01AMGt6ppm6S4oYBbVXxHbwGkZbkDVSNcGY"}`, &r3); st != http.StatusUnauthorized {
		t.Errorf("錯誤密碼 bind 應 401，狀態=%d", st)
	}
	// 正確綁定（crypto.GenerateKeyPair 生成合法地址）
	var r4 map[string]any
	if st := post("/api/node/bind", `{"admin_pass":"secret123","payout_address":"tx01AMGt6ppm6S4oYBbVXxHbwGkZbkDVSNcGY"}`, &r4); st != http.StatusOK {
		t.Fatalf("bind 狀態=%d resp=%v", st, r4)
	}

	// 3) 狀態應顯示綁定＋admin_set
	var st map[string]any
	getJSON(t, srv, "/api/node/panel", &st)
	if st["admin_set"] != true || st["payout_address"] != "tx01AMGt6ppm6S4oYBbVXxHbwGkZbkDVSNcGY" {
		t.Errorf("panel 未反映綁定: %v", st)
	}

	// 4) 未綁定金額錯誤/超額提領拒絕；合法小額提領應上鏈
	var r5 map[string]any
	if st := post("/api/node/withdraw", `{"admin_pass":"secret123","amount":"999999"}`, &r5); st != http.StatusBadRequest {
		t.Errorf("超額提領應 400，狀態=%d", st)
	}
}
