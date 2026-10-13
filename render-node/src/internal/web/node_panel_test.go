package web

// M148：節點架設者面板 API 測試——設置管理密碼 → 綁定收款地址 → 提領（節點私鑰簽名上鏈）。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"

	"tacm/internal/crypto"
	"strings"
	"testing"
)

// cleanNodeConfig 清理測試用 node_config.json（fakeDS.NodeDataDir = os.TempDir）。
func cleanNodeConfig(t *testing.T) {
	t.Helper()
	_ = os.Remove(filepath.Join(os.TempDir(), "node_config.json"))
}

func postJSON(t *testing.T, url, body string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(url, "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	var j map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&j)
	return resp.StatusCode, j
}

func TestNodePanelPages(t *testing.T) {
	cleanNodeConfig(t)
	srv, err := New(newFake())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/node-panel")
	if err != nil {
		t.Fatalf("GET /node-panel: %v", err)
	}
	body := readAllString(resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "節點架設者面板") {
		t.Errorf("/node-panel 狀態=%d 或缺少標題", resp.StatusCode)
	}

	// /api/node/panel 未設置密碼 → admin_set=false、有節點地址。
	st, j := getJSONMap(t, ts.URL+"/api/node/panel")
	if st != http.StatusOK {
		t.Fatalf("/api/node/panel 狀態=%d", st)
	}
	if j["admin_set"] != false {
		t.Errorf("admin_set 應為 false，got %v", j["admin_set"])
	}
	if addr, _ := j["node_address"].(string); addr == "" {
		t.Errorf("node_address 不應為空")
	}
}

func TestNodePanelSetupBindWithdraw(t *testing.T) {
	cleanNodeConfig(t)
	// 生成真實有效 TACm 地址做綁定/提領目標。
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	validAddr, err := kp.Address()
	if err != nil {
		t.Fatalf("Address: %v", err)
	}
	srv, err := New(newFake())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 生成真實有效 TACm 地址做綁定/提領目標。
	// 未設置密碼 → bind 被拒。
	st, _ := postJSON(t, ts.URL+"/api/node/bind", fmt.Sprintf(`{"admin_pass":"x","payout_address":"%s"}`, validAddr))
	if st != http.StatusUnauthorized {
		t.Errorf("未設密碼 bind 應 401，got %d", st)
	}

	// 設置管理密碼（<6 字元應被拒）。
	st, _ = postJSON(t, ts.URL+"/api/node/setup", `{"admin_pass":"123"}`)
	if st != http.StatusBadRequest {
		t.Errorf("短密碼 setup 應 400，got %d", st)
	}

	st, j := postJSON(t, ts.URL+"/api/node/setup", `{"admin_pass":"node-admin-123"}`)
	if st != http.StatusOK {
		t.Fatalf("setup 狀態=%d msg=%v", st, j["message"])
	}

	// 重複設置應被拒。
	st, _ = postJSON(t, ts.URL+"/api/node/setup", `{"admin_pass":"another-123"}`)
	if st != http.StatusBadRequest {
		t.Errorf("重複 setup 應 400，got %d", st)
	}

	// 錯誤密碼 bind 應 401。
	st, _ = postJSON(t, ts.URL+"/api/node/bind", fmt.Sprintf(`{"admin_pass":"wrong","payout_address":"%s"}`, validAddr))
	if st != http.StatusUnauthorized {
		t.Errorf("錯誤密碼 bind 應 401，got %d", st)
	}

	// 正確密碼綁定無效地址應 400。
	st, _ = postJSON(t, ts.URL+"/api/node/bind", `{"admin_pass":"node-admin-123","payout_address":"bad-addr"}`)
	if st != http.StatusBadRequest {
		t.Errorf("無效地址 bind 應 400，got %d", st)
	}

	// 正確綁定。
	st, j = postJSON(t, ts.URL+"/api/node/bind", fmt.Sprintf(`{"admin_pass":"node-admin-123","payout_address":"%s"}`, validAddr))
	if st != http.StatusOK {
		t.Fatalf("bind 狀態=%d msg=%v err=%v", st, j["message"], j["error"])
	}

	// 未綁定時 withdraw（先清綁定再測）→ 400；此處已綁定，測正常提領。
	st, j = postJSON(t, ts.URL+"/api/node/withdraw", `{"admin_pass":"node-admin-123","amount":"10"}`)
	if st != http.StatusOK {
		t.Fatalf("withdraw 狀態=%d msg=%v", st, j["message"])
	}
	if j["tx_hash"] == "" {
		t.Errorf("withdraw 應回 tx_hash")
	}

	// 面板狀態應已綁定＋管理密碼已設置。
	st, j = getJSONMap(t, ts.URL+"/api/node/panel")
	if st != http.StatusOK {
		t.Fatalf("panel 狀態=%d", st)
	}
	if j["payout_address"] != validAddr {
		t.Errorf("payout_address 未更新：%v", j["payout_address"])
	}
	if j["admin_set"] != true {
		t.Errorf("admin_set 應為 true")
	}
}

// TestNodePanelNodeOnlyMode：純節點模式（-serve-dapp=false）下，節點面板仍可用。
func TestNodePanelNodeOnlyMode(t *testing.T) {
	cleanNodeConfig(t)
	srv, err := NewWithNetworkMode(newFake(), "mainnet", false, 3) // 純節點模式（-serve-dapp=false）
	if err != nil {
		t.Fatalf("NewWithNetworkMode: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/node-panel")
	if err != nil {
		t.Fatalf("GET /node-panel: %v", err)
	}
	body := readAllString(resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "節點架設者面板") {
		t.Errorf("純節點模式 /node-panel 狀態=%d 或缺少標題", resp.StatusCode)
	}

	st, j := getJSONMap(t, ts.URL+"/api/node/panel")
	if st != http.StatusOK {
		t.Fatalf("純節點模式 /api/node/panel 狀態=%d", st)
	}
	if j["node_id"] == nil || j["node_address"] == nil {
		t.Errorf("panel 缺 node_id/node_address")
	}

	// 純節點模式不該有 DApp 路由（錢包）。
	resp2, err := http.Get(ts.URL + "/wallet")
	if err != nil {
		t.Fatalf("GET /wallet: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode == http.StatusOK {
		t.Errorf("純節點模式不應提供 /wallet（DApp 路由）")
	}

	// 純節點模式頁面精簡：底部無 DApp 導航（錢包/挖礦/交易所）、無登入膠囊、無工具按鈕；
	// 頂部有節點導航（節點監控/節點面板）。
	resp3, err := http.Get(ts.URL + "/node-panel")
	if err != nil {
		t.Fatalf("GET /node-panel: %v", err)
	}
	b3 := readAllString(resp3)
	resp3.Body.Close()
	if strings.Contains(b3, "href=\"/mining\"") || strings.Contains(b3, "data-i18n=\"nav.exchange\"") || strings.Contains(b3, "href=\"/auth/login\"") || strings.Contains(b3, "id=\"toolsToggle\"") {
		t.Errorf("純節點模式頁面仍含 DApp 導航/登入/工具：挖礦=%v 交易所=%v 登入=%v 工具=%v",
			strings.Contains(b3, "href=\"/mining\""), strings.Contains(b3, "data-i18n=\"nav.exchange\""),
			strings.Contains(b3, "href=\"/auth/login\""), strings.Contains(b3, "id=\"toolsToggle\""))
	}
	if !strings.Contains(b3, "href=\"/node-panel\"") || !strings.Contains(b3, "href=\"/network\"") {
		t.Errorf("純節點模式頁面應有節點導航（節點面板/節點監控）")
	}
}

// getJSONMap GET 並解析 JSON。
func getJSONMap(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	var j map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&j)
	return resp.StatusCode, j
}
