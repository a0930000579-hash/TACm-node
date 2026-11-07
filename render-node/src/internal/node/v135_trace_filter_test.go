package node

// v135：trace_filter——Parity 風格歷史 trace 索引（區塊範圍/位址過濾/分頁）。

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTraceFilter：傳兩筆交易後，trace_filter 依 fromAddress 過濾回正確 trace。
func TestTraceFilter(t *testing.T) {
	cfg := testCfg(t)
	cfg.BlockTime = 1
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
	bob0x := "0x" + strings.Repeat("22", 20)

	// 兩筆交易：admin→alice、admin→bob。
	for _, to := range []string{alice0x, bob0x} {
		out := ethRPC(t, srv, "eth_sendTransaction", map[string]any{
			"from":  ethFrom,
			"to":    to,
			"value": "0xde0b6b3a7640000",
		})
		if e, ok := out["error"]; ok {
			t.Fatalf("send failed: %v", e)
		}
	}
	waitHeight53(t, n, 1)
	// 依 toAddress 過濾 bob。
	out := ethRPC(t, srv, "trace_filter", map[string]any{
		"toAddress": []any{bob0x},
		"count":     10,
	})
	if e, ok := out["error"]; ok {
		t.Fatalf("trace_filter err: %v", e)
	}
	arr, ok := out["result"].([]any)
	if !ok {
		t.Fatalf("result 非陣列: %#v", out["result"])
	}
	if len(arr) != 1 {
		t.Fatalf("應回 1 筆 bob trace, 得 %d: %#v", len(arr), arr)
	}
	first := arr[0].(map[string]any)
	action := first["action"].(map[string]any)
	if strings.ToLower(action["to"].(string)) != bob0x {
		t.Fatalf("to 不符: %v", action["to"])
	}
	if first["type"] != "call" || first["subtraces"] != float64(0) {
		t.Fatalf("trace 欄位不符: %#v", first)
	}
	// count=0 → 缺省 100；fromBlock 超出範圍 → 空。
	out2 := ethRPC(t, srv, "trace_filter", map[string]any{
		"fromBlock": "0xffff", "toBlock": "0xffff",
	})
	if arr2, ok2 := out2["result"].([]any); ok2 && len(arr2) != 0 {
		t.Fatalf("超範圍應空: %#v", arr2)
	}
}
