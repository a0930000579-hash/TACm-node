package node

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// M139 合約驗證中心：/contract/read 只讀互動 e2e。
//  1. 部署標準代幣（節點代簽）
//  2. totalSupply() → uint256 解碼
//  3. balanceOf(address) → uint256 解碼
//  4. name() → string 解碼
//  5. 錯誤路徑（未知合約 / 參數數量不符）

func TestContractReadInteractive(t *testing.T) {
	n, srv, admin := contractStudioSetup(t)

	out := studioPostJSON(t, srv, "/contract/deploy", map[string]any{
		"name": "ReadToken", "symbol": "RDT", "supply": "250000",
	})
	if out["ok"] != true {
		t.Fatalf("部署失敗: %v", out)
	}
	contractAddr := out["contract_address"].(string)
	contract0x, err := tx0To0x(contractAddr)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		info := n.contracts.Get(contract0x)
		return info != nil && info.CodeSize > 0
	}, 8*time.Second, "合約未入塊部署")

	// totalSupply()
	r1 := readRPC(t, srv, map[string]any{
		"address": contractAddr, "method_sig": "totalSupply()",
		"inputs": []string{}, "args": []any{}, "outputs": []string{"uint256"},
	})
	if r1["ok"] != true {
		t.Fatalf("totalSupply read 失敗: %v", r1)
	}
	outs, _ := r1["outputs"].([]any)
	if len(outs) != 1 || outs[0] != "250000" {
		t.Fatalf("totalSupply 解碼不符: %v", outs)
	}

	// balanceOf(admin)
	r2 := readRPC(t, srv, map[string]any{
		"address": contractAddr, "method_sig": "balanceOf(address)",
		"inputs": []string{"address"}, "args": []any{admin},
		"outputs": []string{"uint256"},
	})
	if r2["ok"] != true {
		t.Fatalf("balanceOf read 失敗: %v", r2)
	}
	outs2, _ := r2["outputs"].([]any)
	if len(outs2) != 1 || outs2[0] != "250000" {
		t.Fatalf("balanceOf 解碼不符: %v", outs2)
	}

	// name() → string
	r3 := readRPC(t, srv, map[string]any{
		"address": contractAddr, "method_sig": "name()",
		"inputs": []string{}, "args": []any{}, "outputs": []string{"string"},
	})
	if r3["ok"] != true {
		t.Fatalf("name read 失敗: %v", r3)
	}
	outs3, _ := r3["outputs"].([]any)
	if len(outs3) != 1 || outs3[0] != "ReadToken" {
		t.Fatalf("name 解碼不符: %v", outs3)
	}

	// 錯誤：未知合約
	resp, err := http.Post(srv.URL+"/contract/read", "application/json",
		strings.NewReader(`{"address":"0x000000000000000000000000000000000000dead","method_sig":"name()","inputs":[],"args":[],"outputs":["string"]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("未知合約應 404，得到 %d", resp.StatusCode)
	}

	// 錯誤：參數數量不符
	r4 := readRPC(t, srv, map[string]any{
		"address": contractAddr, "method_sig": "balanceOf(address)",
		"inputs": []string{"address"}, "args": []any{}, "outputs": []string{"uint256"},
	})
	if r4["ok"] == true {
		t.Fatalf("參數數量不符應失敗: %v", r4)
	}
	if !strings.Contains(r4["error"].(string), "數量") {
		t.Fatalf("錯誤訊息應說明數量不符: %v", r4["error"])
	}
}

func readRPC(t *testing.T, srv *httptest.Server, body map[string]any) map[string]any {
	t.Helper()
	return studioPostJSON(t, srv, "/contract/read", body)
}
