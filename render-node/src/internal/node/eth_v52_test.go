package node

// eth_v52_test.go — v52：debug_getRawTransaction / debug_getRawBlock / admin_peers。
// 主流鏈 debug 面與 admin 面補齊（原始交易/區塊 JSON、P2P 對等節點檢視）。

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDebugRawTransaction：eth_sendTransaction 入塊後，debug_getRawTransaction
// 回原始交易 JSON（含 from/to/amount/signature）；不存在回錯誤。
func TestDebugRawTransaction(t *testing.T) {
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
	ethFrom, _ := n.keypair.EthAddress()
	alice0x := "0x" + strings.Repeat("11", 20)

	out := ethRPC(t, srv, "eth_sendTransaction", map[string]any{
		"from":  ethFrom,
		"to":    alice0x,
		"value": "0xde0b6b3a7640000",
	})
	if e, ok := out["error"]; ok {
		t.Fatalf("eth_sendTransaction error: %v", e)
	}
	txHash, _ := out["result"].(string)
	if txHash == "" {
		t.Fatal("txHash 不應為空")
	}

	// debug_getRawTransaction 回原始 JSON 字串，含完整欄位。
	rawOut := ethRPC(t, srv, "debug_getRawTransaction", txHash)
	if e, ok := rawOut["error"]; ok {
		t.Fatalf("debug_getRawTransaction error: %v", e)
	}
	rawStr, _ := rawOut["result"].(string)
	if rawStr == "" {
		t.Fatal("raw 不應為空")
	}
	var rawTx map[string]any
	if err := json.Unmarshal([]byte(rawStr), &rawTx); err != nil {
		t.Fatalf("raw 非 JSON: %v", err)
	}
	if rawTx["from"] == nil || rawTx["to"] == nil || rawTx["signature"] == nil {
		t.Fatalf("raw 缺關鍵欄位: %v", rawTx)
	}

	// 不存在的交易 → 錯誤。
	bad := ethRPC(t, srv, "debug_getRawTransaction", "0x"+strings.Repeat("00", 32))
	if _, ok := bad["error"]; !ok {
		t.Fatalf("不存在的交易應回錯誤: %v", bad)
	}
}

// TestDebugRawBlock：debug_getRawBlock("latest") 回區塊原始 JSON（含交易陣列）。
func TestDebugRawBlock(t *testing.T) {
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

	rawOut := ethRPC(t, srv, "debug_getRawBlock", "latest")
	if e, ok := rawOut["error"]; ok {
		t.Fatalf("debug_getRawBlock error: %v", e)
	}
	rawStr, _ := rawOut["result"].(string)
	if rawStr == "" {
		t.Fatal("raw 不應為空")
	}
	var blk map[string]any
	if err := json.Unmarshal([]byte(rawStr), &blk); err != nil {
		t.Fatalf("raw 非 JSON: %v", err)
	}
	if blk["height"] == nil || blk["transactions"] == nil {
		t.Fatalf("區塊 raw 缺欄位: %v", blk)
	}
}

// TestAdminPeers：未啟用 P2P 時回空陣列（非錯誤）。
func TestAdminPeers(t *testing.T) {
	cfg := testCfg(t)
	n, err := New(cfg, "node1", 1)
	if err != nil {
		t.Fatal(err)
	}
	insertGenesisWithAlloc(t, n, "tx01TESTNODEADDR0000000000000000000000")
	n.Start()
	srv := httptest.NewServer(NewRPCServer(n).mux)
	t.Cleanup(func() {
		srv.Close()
		_ = n.Close()
	})
	out := ethRPC(t, srv, "admin_peers")
	if e, ok := out["error"]; ok {
		t.Fatalf("admin_peers error: %v", e)
	}
	peers, ok := out["result"].([]any)
	if !ok {
		t.Fatalf("admin_peers 應回陣列: %v", out["result"])
	}
	if len(peers) != 0 {
		t.Fatalf("無 P2P 應為空陣列: %v", peers)
	}
}
