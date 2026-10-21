package node

// v136：debug_getRawReceipts / debug_storageRangeAt（單一節點共用，避免 fd 累積）。

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestV136DebugEndpoints：send tx 入塊 → debug_getRawReceipts 回收據 → storageRangeAt 枚舉。
func TestV136DebugTraceEndpoints(t *testing.T) {
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
	t.Cleanup(func() { _ = n.Close() })
	srv := httptest.NewServer(NewRPCServer(n).mux)
	t.Cleanup(srv.Close)

	ethFrom, _ := n.keypair.EthAddress()
	alice0x := "0x" + strings.Repeat("11", 20)
	out := ethRPC(t, srv, "eth_sendTransaction", map[string]any{
		"from": ethFrom, "to": alice0x, "value": "0xde0b6b3a7640000",
	})
	if e, ok := out["error"]; ok {
		t.Fatalf("send failed: %v", e)
	}
	waitHeight53(t, n, 1)

	// 1) debug_getRawReceipts
	res := ethRPC(t, srv, "debug_getRawReceipts", "latest")
	if e, ok := res["error"]; ok {
		t.Fatalf("debug_getRawReceipts err: %v", e)
	}
	arr, ok := res["result"].([]any)
	if !ok || len(arr) == 0 {
		t.Fatalf("應有收據: %#v", res["result"])
	}
	first := arr[0].(map[string]any)
	if _, ok2 := first["transactionHash"]; !ok2 {
		t.Fatalf("收據缺 transactionHash: %#v", first)
	}

	// 2) debug_storageRangeAt（無合約位址 → 空 storage + nextKey null）
	res2 := ethRPC(t, srv, "debug_storageRangeAt",
		"latest", 0, alice0x, "0x00", 10)
	if e, ok := res2["error"]; ok {
		t.Fatalf("storageRangeAt err: %v", e)
	}
	m, ok := res2["result"].(map[string]any)
	if !ok {
		t.Fatalf("result 非 map: %#v", res2["result"])
	}
	if m["nextKey"] != nil {
		t.Fatalf("空 storage nextKey 應為 null: %#v", m)
	}
}
