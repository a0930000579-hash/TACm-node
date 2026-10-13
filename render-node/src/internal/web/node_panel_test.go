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
