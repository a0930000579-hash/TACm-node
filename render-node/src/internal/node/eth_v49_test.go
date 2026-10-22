package node

import (
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestEthCallStateOverride：eth_call 第三參數 state override——參數解析正常、
// 不影響鏈上狀態（部署合約 call 正常；帶 override 仍成功）。
func TestEthCallStateOverride(t *testing.T) {
	n, srv, admin := contractStudioSetup(t)
	admin0x, _ := tx0To0x(admin)

	out := studioPostJSON(t, srv, "/contract/deploy", map[string]any{
		"name": "OVR", "symbol": "OV", "supply": "1000",
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

	// 正常 call（無 override）：symbol() selector 0x06fdde03 → 非空回傳。
	got := ethResult(t, srv, "eth_call", map[string]any{"to": contract0x, "data": "0x06fdde03"}, "latest")
	if got == nil {
		t.Fatal("eth_call 無 override 失敗")
	}
	if _, ok := got.(string); !ok || got == "0x" {
		t.Fatalf("symbol 應回 ABI 字串, got %v", got)
	}

	// 帶第三參數 override（無關地址 balance/nonce）：仍成功且無副作用。
	overrides := map[string]any{
		admin0x: map[string]any{"balance": "0x10", "nonce": "0x1"},
	}
	got2 := ethResult(t, srv, "eth_call", map[string]any{"to": contract0x, "data": "0x06fdde03"}, "latest", overrides)
	if _, ok := got2.(string); !ok || got2 == "0x" {
		t.Fatalf("帶 override 的 eth_call 失敗: %v", got2)
	}
	if got2 != got {
		t.Fatalf("無關地址 override 不應改變結果: %v vs %v", got, got2)
	}
	// 鏈上狀態未被覆蓋（eth_getBalance admin 仍為創世餘額＋挖礦獎勵，非 0x10）。
	bal, ok := ethResult(t, srv, "eth_getBalance", admin0x).(string)
	if !ok || bal == "0x10" {
		t.Fatalf("override 污染鏈上狀態: %v", bal)
	}
}

// TestEthSendTransaction：eth_sendTransaction 節點金鑰代簽轉帳 → 入塊 → 查詢驗證；
// 非節點金鑰 from 拒絕。
func TestEthSendTransaction(t *testing.T) {
	cfg := testCfg(t)
	n, err := New(cfg, "node1", 1)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := n.keypair.Address()
	if err != nil {
		t.Fatal(err)
	}
	insertGenesisWithAlloc(t, n, admin)
	n.Start()
	srv := httptest.NewServer(NewRPCServer(n).mux)
	t.Cleanup(func() {
		srv.Close()
		_ = n.Close()
	})
	admin0x, _ := tx0To0x(admin)
	// eth_accounts 回 keccak 以太地址；eth_sendTransaction from 必須與之一致。
	ethFrom, _ := n.keypair.EthAddress()
	alice0x := "0x" + strings.Repeat("11", 20)
	const valWei = 5_000_000_000_000_000_000

	// 1. 非節點金鑰 from → 拒絕。
	bad := ethRPC(t, srv, "eth_sendTransaction", map[string]any{
		"from":  alice0x,
		"to":    alice0x,
		"value": "0x1",
	})
	if _, ok := bad["error"]; !ok {
		t.Fatalf("非節點金鑰 from 應被拒: %v", bad)
	}

	// 2. 節點金鑰代簽轉帳（value=5e18 wei，from=eth_accounts 同源地址）。
	out := ethRPC(t, srv, "eth_sendTransaction", map[string]any{
		"from":  ethFrom,
		"to":    alice0x,
		"value": "0x" + strconv.FormatInt(valWei, 16),
	})
	if e, ok := out["error"]; ok {
		t.Fatalf("eth_sendTransaction error: %v", e)
	}
	txHash, _ := out["result"].(string)
	if txHash == "" {
		t.Fatal("無 tx hash")
	}
	waitFor(t, func() bool {
		tx, err := n.db.GetTransaction(txHash)
		return err == nil && tx != nil && tx.Status == "confirmed"
	}, 20*time.Second, "eth_sendTransaction 未入塊")

	got := ethResult(t, srv, "eth_getTransactionByHash", "0x"+txHash)
	m, _ := got.(map[string]any)
	if m == nil {
		t.Fatalf("查詢失敗: %v", got)
	}
	if m["from"] != admin0x {
		t.Fatalf("from=%v 期望 %v", m["from"], admin0x)
	}
	if m["to"] != alice0x {
		t.Fatalf("to=%v 期望 %v", m["to"], alice0x)
	}
	if m["value"] != "0x"+strconv.FormatInt(valWei, 16) {
		t.Fatalf("value=%v 期望 5e18 wei", m["value"])
	}
	if m["blockHash"] == nil {
		t.Fatal("交易應已入塊（blockHash 非 null）")
	}
}
