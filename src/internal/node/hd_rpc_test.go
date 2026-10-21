package node

// v58：HD 錢包 RPC 測試（/wallet/hd/new＋/wallet/hd/derive）。
// 覆蓋：BIP-44 首帳戶生成、路徑派生確定性、參數錯誤處理。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func hdTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	n, err := New(testCfg(t), "hd", 1)
	if err != nil {
		t.Fatal(err)
	}
	insertGenesis(t, n)
	n.Start()
	t.Cleanup(func() { _ = n.Close() })
	rpc := NewRPCServer(n)
	return httptest.NewServer(rpc.Handler())
}

// TestHDWalletNewRPC GET /wallet/hd/new：12 詞助記詞＋BIP-44 首帳戶。
func TestHDWalletNewRPC(t *testing.T) {
	ts := hdTestServer(t)
	defer ts.Close()

	res, err := http.Get(ts.URL + "/wallet/hd/new")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		Ok       bool   `json:"ok"`
		Mnemonic string `json:"mnemonic"`
		Path     string `json:"path"`
		Account  struct {
			Address string `json:"address"`
			WIF     string `json:"wif"`
		} `json:"account"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("解析失敗: %v", err)
	}
	if !out.Ok {
		t.Fatalf("ok=false")
	}
	if got := len(strings.Fields(out.Mnemonic)); got != 12 {
		t.Fatalf("助記詞詞數 = %d, want 12", got)
	}
	if out.Path != "m/44'/60'/0'/0/0" {
		t.Fatalf("路徑 = %q, want m/44'/60'/0'/0/0", out.Path)
	}
	if out.Account.Address == "" || out.Account.WIF == "" {
		t.Fatalf("帳戶不完整")
	}
}

// TestHDWalletDeriveRPC POST /wallet/hd/derive：確定性＋參數錯誤。
func TestHDWalletDeriveRPC(t *testing.T) {
	ts := hdTestServer(t)
	defer ts.Close()

	// 先拿新助記詞。
	res, err := http.Get(ts.URL + "/wallet/hd/new")
	if err != nil {
		t.Fatal(err)
	}
	var nw struct {
		Mnemonic string `json:"mnemonic"`
	}
	_ = json.NewDecoder(res.Body).Decode(&nw)
	res.Body.Close()

	derive := func(body string) (string, map[string]any, int) {
		resp, err := http.Post(ts.URL+"/wallet/hd/derive", "application/json", bytes.NewReader([]byte(body)))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer resp.Body.Close()
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		return resp.Status, m, resp.StatusCode
	}

	// account=0/index=0 兩次一致（確定性）。
	b1 := `{"mnemonic":"` + nw.Mnemonic + `","account":0,"index":0}`
	st1, m1, c1 := derive(b1)
	if c1 != http.StatusOK || st1 != "200 OK" {
		t.Fatalf("derive1 HTTP %d", c1)
	}
	b2 := `{"mnemonic":"` + nw.Mnemonic + `","account":0,"index":0}`
	_, m2, _ := derive(b2)
	a1, _ := m1["account"].(map[string]any)
	a2, _ := m2["account"].(map[string]any)
	if a1["address"] != a2["address"] {
		t.Fatalf("BIP-44 派生不確定")
	}
	if a1["address"] == "" {
		t.Fatalf("地址為空")
	}

	// account=1 應不同地址。
	b3 := `{"mnemonic":"` + nw.Mnemonic + `","account":1,"index":0}`
	_, m3, _ := derive(b3)
	a3, _ := m3["account"].(map[string]any)
	if a3["address"] == a1["address"] {
		t.Fatalf("account=1 不應與 account=0 相同")
	}

	// 空助記詞 → 400。
	_, _, c := derive(`{"mnemonic":"","account":0,"index":0}`)
	if c != http.StatusBadRequest {
		t.Fatalf("空助記詞 HTTP %d, want 400", c)
	}
	// 非法路徑 → 400。
	_, _, c = derive(`{"mnemonic":"` + nw.Mnemonic + `","path":"x/0"}`)
	if c != http.StatusBadRequest {
		t.Fatalf("非法路徑 HTTP %d, want 400", c)
	}
	// 負 index → 400。
	_, _, c = derive(`{"mnemonic":"` + nw.Mnemonic + `","account":0,"index":-1}`)
	if c != http.StatusBadRequest {
		t.Fatalf("負 index HTTP %d, want 400", c)
	}
}
