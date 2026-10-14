package node

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tacm/internal/crypto"
	"tacm/internal/vm"
)

// 本檔驗證 M44 代幣發行閉環（經 RPC 層）：
//  1. POST /contract/deploy 節點金鑰代簽發行標準代幣（name/symbol/supply）
//  2. 交易入塊後合約真實部署（code 寫入）
//  3. GET /contract/list 回傳 supply/name/symbol
//  4. GET /contract/call/{addr} 只讀模擬查詢（totalSupply/balanceOf/name/symbol）
//  5. POST /contract/call 節點代簽鏈上 transfer，入塊後餘額變動

// postJSON 對測試 server 發送 JSON POST。
func studioPostJSON(t *testing.T, ts *httptest.Server, path string, body any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(ts.URL+path, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST %s 失敗: %v", path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("解析 %s 響應失敗: %v", path, err)
	}
	return out
}

// getJSON 對測試 server 發送 GET。
func studioGetJSON(t *testing.T, ts *httptest.Server, path string) map[string]any {
	t.Helper()
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatalf("GET %s 失敗: %v", path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("解析 %s 響應失敗: %v", path, err)
	}
	return out
}

// contractStudioSetup 建立帶 RPC server 的節點。
func contractStudioSetup(t *testing.T) (*Node, *httptest.Server, string) {
	t.Helper()
	n, err := New(testCfg(t), "node1", 1)
	if err != nil {
		t.Fatal(err)
	}
	aliceKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	alice, _ := aliceKP.Address()
	insertGenesisWithAlloc(t, n, alice)
	n.Start()
	srv := httptest.NewServer(NewRPCServer(n).mux)
	t.Cleanup(func() {
		srv.Close()
		_ = n.Close()
	})
	// 節點金鑰地址（代簽發行者）。
	admin, err := n.keypair.Address()
	if err != nil {
		t.Fatal(err)
	}
	return n, srv, admin
}

func TestContractStudioDeployAndQuery(t *testing.T) {
	n, srv, admin := contractStudioSetup(t)

	// 1. 發行代幣。
	out := studioPostJSON(t, srv, "/contract/deploy", map[string]any{
		"name": "MyToken", "symbol": "TAC", "supply": "1000000",
	})
	if out["ok"] != true {
		t.Fatalf("部署失敗: %v", out)
	}
	contractAddr := out["contract_address"].(string)
	txHash := out["tx_hash"].(string)
	t.Logf("deploy tx=%s contract=%s admin=%s", txHash, contractAddr, admin)

	// 2. 等待合約入塊（真實部署）。
	contract0x, err := tx0To0x(contractAddr)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		info := n.contracts.Get(contract0x)
		return info != nil && info.CodeSize > 0
	}, 8*time.Second, "合約未入塊部署")

	// 3. 列表帶元資料。
	info := studioGetJSON(t, srv, "/contract/list")
	contracts, _ := info["contracts"].([]any)
	found := false
	for _, c := range contracts {
		m := c.(map[string]any)
		if m["address"] == contractAddr {
			found = true
			if m["supply"] != "1000000" {
				t.Fatalf("supply 應為 1000000，得到 %v", m["supply"])
			}
			if m["name"] != "MyToken" {
				t.Fatalf("name 應為 MyToken，得到 %v", m["name"])
			}
			if m["symbol"] != "TAC" {
				t.Fatalf("symbol 應為 TAC，得到 %v", m["symbol"])
			}
		}
	}
	if !found {
		t.Fatal("列表未包含新合約")
	}

	// 4. 只讀模擬查詢。
	supply := studioGetJSON(t, srv, "/contract/call/"+contractAddr+"?calldata="+vmHex(vm.Erc20TotalSupplyCalldata()))
	if supply["ok"] != true || supply["return_data"] != "00000000000000000000000000000000000000000000000000000000000f4240" {
		t.Fatalf("totalSupply 查詢異常: %v", supply)
	}
	name := studioGetJSON(t, srv, "/contract/call/"+contractAddr+"?calldata="+vmHex(vm.Erc20NameCalldata()))
	if name["ok"] != true || !strings.Contains(name["return_data"].(string), "4d79546f6b656e") {
		t.Fatalf("name 查詢異常: %v", name)
	}
	symbol := studioGetJSON(t, srv, "/contract/call/"+contractAddr+"?calldata="+vmHex(vm.Erc20SymbolCalldata()))
	if symbol["ok"] != true || !strings.Contains(symbol["return_data"].(string), "544143") {
		t.Fatalf("symbol 查詢異常: %v", symbol)
	}
	// 節點金鑰餘額應等於 totalSupply（creator=admin）。
	bal := studioGetJSON(t, srv, "/contract/call/"+contractAddr+"?from="+admin+"&calldata="+vmHex(vm.Erc20BalanceOfCalldata(admin)))
	if bal["ok"] != true || bal["return_data"] != "00000000000000000000000000000000000000000000000000000000000f4240" {
		t.Fatalf("admin 餘額查詢異常: %v", bal)
	}
}

func TestContractStudioTransferOnChain(t *testing.T) {
	n, srv, admin := contractStudioSetup(t)

	// 發行 1000000 TAC。
	out := studioPostJSON(t, srv, "/contract/deploy", map[string]any{
		"name": "TAC Gold", "symbol": "TG", "supply": "1000000",
	})
	contractAddr := out["contract_address"].(string)
	contract0x, _ := tx0To0x(contractAddr)
	waitFor(t, func() bool {
		info := n.contracts.Get(contract0x)
		return info != nil && info.CodeSize > 0
	}, 8*time.Second, "合約未入塊部署")

	// 目標地址（無初始餘額）。注意：calldata 內地址必須為 0x 格式（addrToInt 僅解 0x）。
	aliceKP, _ := crypto.GenerateKeyPair()
	alice, _ := aliceKP.Address()
	alice0x, _ := tx0To0x(alice)
	admin0x, _ := tx0To0x(admin)

	// 節點代簽 transfer(admin → alice 250)。
	calldata := vm.Erc20TransferCalldata(alice0x, big.NewInt(250))
	callOut := studioPostJSON(t, srv, "/contract/call", map[string]any{
		"to": contractAddr, "calldata": vmHex(calldata),
	})
	if callOut["ok"] != true {
		t.Fatalf("呼叫交易被拒: %v", callOut)
	}
	t.Logf("transfer tx=%s admin→%s 250", callOut["tx_hash"], alice)

	// 等待 transfer 入塊執行。
	waitFor(t, func() bool {
		bal := n.contracts.StorageAt(contract0x, vmAddrInt(alice0x))
		return bal.Cmp(big.NewInt(250)) == 0
	}, 8*time.Second, "transfer 未入塊")

	// 鏈上驗證。
	bal := studioGetJSON(t, srv, "/contract/call/"+contractAddr+"?from="+admin+"&calldata="+vmHex(vm.Erc20BalanceOfCalldata(alice0x)))
	if bal["ok"] != true || bal["return_data"] != "00000000000000000000000000000000000000000000000000000000000000fa" {
		t.Fatalf("alice 餘額應為 250(0xfa)，得到 %v", bal)
	}
	balAdmin := studioGetJSON(t, srv, "/contract/call/"+contractAddr+"?from="+admin+"&calldata="+vmHex(vm.Erc20BalanceOfCalldata(admin0x)))
	if balAdmin["ok"] != true || balAdmin["return_data"] != "00000000000000000000000000000000000000000000000000000000000f4146" {
		t.Fatalf("admin 餘額應為 999750(0xf4146)，得到 %v", balAdmin)
	}
}

// vmHex 包裝 hex 編碼（vm 套件內函數）。
func vmHex(b []byte) string { return hex.EncodeToString(b) }

// vmAddrInt 地址 → storage key（與 vm.addrToInt 一致：hex 解碼）。
func vmAddrInt(addr string) *big.Int {
	s := addr
	if strings.HasPrefix(s, "tx0") {
		// tx0 格式：轉 0x 後 hex 解碼（測試用簡化：取後 40 hex）
		s = strings.TrimPrefix(s, "tx0")
	}
	s = strings.TrimPrefix(s, "0x")
	v, ok := new(big.Int).SetString(s, 16)
	if !ok {
		return big.NewInt(0)
	}
	return v
}

var _ = fmt.Sprintf

// TestContractStudioERC20Convenience 驗證便利端點：詳情查詢＋代簽轉帳（前端免組 calldata）。
func TestContractStudioERC20Convenience(t *testing.T) {
	n, srv, admin := contractStudioSetup(t)

	out := studioPostJSON(t, srv, "/contract/deploy", map[string]any{
		"name": "Convenience", "symbol": "CV", "supply": "5000",
	})
	if out["ok"] != true {
		t.Fatalf("部署失敗: %v", out)
	}
	contractAddr := out["contract_address"].(string)
	contract0x, _ := tx0To0x(contractAddr)
	waitFor(t, func() bool {
		info := n.contracts.Get(contract0x)
		return info != nil && info.CodeSize > 0
	}, 8*time.Second, "合約未入塊部署")

	// 詳情查詢（holder=admin）——supply/name/symbol/balance。
	info := studioGetJSON(t, srv, "/contract/erc20/"+contractAddr+"?holder="+admin)
	if info["ok"] != true || info["supply"] != "5000" || info["name"] != "Convenience" || info["symbol"] != "CV" || info["balance"] != "5000" {
		t.Fatalf("詳情查詢異常: %v", info)
	}

	// 便利轉帳（admin → alice 77）。
	aliceKP, _ := crypto.GenerateKeyPair()
	alice, _ := aliceKP.Address()
	tr := studioPostJSON(t, srv, "/contract/erc20/transfer", map[string]any{
		"contract": contractAddr, "to": alice, "amount": "77",
	})
	if tr["ok"] != true {
		t.Fatalf("便利轉帳被拒: %v", tr)
	}
	alice0x, _ := tx0To0x(alice)
	waitFor(t, func() bool {
		bal := n.contracts.StorageAt(contract0x, vmAddrInt(alice0x))
		return bal.Cmp(big.NewInt(77)) == 0
	}, 8*time.Second, "便利轉帳未入塊")

	info2 := studioGetJSON(t, srv, "/contract/erc20/"+contractAddr+"?holder="+alice)
	if info2["balance"] != "77" {
		t.Fatalf("alice 餘額應為 77，得到 %v", info2["balance"])
	}
	info3 := studioGetJSON(t, srv, "/contract/erc20/"+contractAddr+"?holder="+admin)
	if info3["balance"] != "4923" {
		t.Fatalf("admin 餘額應為 4923，得到 %v", info3["balance"])
	}
}

// TestContractStudioGasLimit 驗證 M48 RPC gas_limit：正常值入塊、過小被模擬拒絕。
func TestContractStudioGasLimit(t *testing.T) {
	n, srv, _ := contractStudioSetup(t)
	defer func() { _ = n.Close() }()

	// 1. gas_limit=5000000 正常部署入塊。
	out := studioPostJSON(t, srv, "/contract/deploy", map[string]any{
		"name": "GasCoin", "symbol": "GAS", "supply": "500000",
		"gas_limit": 5000000,
	})
	if out["ok"] != true {
		t.Fatalf("gas_limit=5M 部署失敗: %v", out)
	}
	contractAddr := out["contract_address"].(string)
	waitFor(t, func() bool {
		info := n.contracts.Get(must0x(t, contractAddr))
		return info != nil && info.CodeSize > 0
	}, 8*time.Second, "gas_limit 部署未入塊")

	// 2. gas_limit=100 過小：入池前模擬 OOG，RPC 應回 400。
	raw, err := json.Marshal(map[string]any{
		"name": "BadCoin", "symbol": "BAD", "supply": "1000",
		"gas_limit": 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(srv.URL+"/contract/deploy", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("gas_limit=100 應被拒絕(400)，得到 %d", resp.StatusCode)
	}

	// 3. 過大 gas_limit（>10M）應被拒絕。
	resp2, err := http.Post(srv.URL+"/contract/deploy", "application/json",
		bytes.NewReader([]byte(`{"name":"X","symbol":"X","supply":"1000","gas_limit":99999999}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("gas_limit=99M 應被拒絕(400)，得到 %d", resp2.StatusCode)
	}
}

func must0x(t *testing.T, tx0 string) string {
	t.Helper()
	evm, err := tx0To0x(tx0)
	if err != nil {
		t.Fatal(err)
	}
	return evm
}
