package node

// v53-C1-C3：主流鏈 RPC 補齊測試。
//   - debug_traceBlockByNumber / debug_traceBlockByHash（區塊內全部交易追蹤摘要）
//   - txpool_inspect（pending[from][nonce] 字串欄位）
//   - eth_getBlockByNumber safe/finalized 標籤

import (
	"math/big"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestRPCServer(t *testing.T) (*Node, *httptest.Server) {
	t.Helper()
	n, err := New(testCfg(t), "v53-node", 1)
	if err != nil {
		t.Fatal(err)
	}
	insertGenesis(t, n)
	n.Start()
	t.Cleanup(func() { _ = n.Close() })
	srv := httptest.NewServer(NewRPCServer(n).mux)
	t.Cleanup(srv.Close)
	return n, srv
}

// waitHeight 等待節點出塊到指定高度。
func waitHeight53(t *testing.T, n *Node, h int64) {
	t.Helper()
	waitFor(t, func() bool { return n.DB().GetTipHeight() >= h }, 15*time.Second, "節點未出塊")
}

func TestDebugTraceBlockByNumber(t *testing.T) {
	n, srv := newTestRPCServer(t)
	waitHeight53(t, n, 2)

	out := ethRPC(t, srv, "debug_traceBlockByNumber", "latest")
	if out["error"] != nil {
		t.Fatalf("debug_traceBlockByNumber latest 失敗: %v", out["error"])
	}
	arr, ok := out["result"].([]any)
	if !ok {
		t.Fatalf("result 非陣列: %T", out["result"])
	}
	if len(arr) == 0 {
		t.Fatal("latest 區塊應至少回空陣列（非 nil）")
	}
	// hex 高度
	hexH := "0x" + strings.TrimLeft(strconv53(n.DB().GetTipHeight()), "0")
	if hexH == "0x" {
		hexH = "0x0"
	}
	out2 := ethRPC(t, srv, "debug_traceBlockByNumber", hexH)
	if out2["error"] != nil {
		t.Fatalf("debug_traceBlockByNumber hex 失敗: %v", out2["error"])
	}
}

func TestDebugTraceBlockByHash(t *testing.T) {
	n, srv := newTestRPCServer(t)
	waitHeight53(t, n, 2)
	tip, err := n.DB().GetBlock(n.DB().GetTipHeight())
	if err != nil || tip == nil {
		t.Fatal("讀 tip 失敗")
	}
	out := ethRPC(t, srv, "debug_traceBlockByHash", "0x"+tip.Hash)
	if out["error"] != nil {
		t.Fatalf("debug_traceBlockByHash 失敗: %v", out["error"])
	}
	if _, ok := out["result"].([]any); !ok {
		t.Fatalf("result 非陣列: %T", out["result"])
	}
	// 不存在的哈希 → error
	bad := ethRPC(t, srv, "debug_traceBlockByHash", "0x"+strings.Repeat("11", 32))
	if bad["error"] == nil {
		t.Fatal("不存在哈希應回 error")
	}
}

func TestTxpoolInspect(t *testing.T) {
	n, srv := newTestRPCServer(t)
	// 塞一筆 pending 交易（不打包，模擬 mempool 內容）。
	payload := map[string]any{
		"from": "tx01alice", "to": "tx01bob", "amount": "10", "fee": "0.01",
		"nonce": int64(0), "gas_limit": "21000",
	}
	if err := n.db.AddMempoolTx("v53pendingtx1", payload, "sig", "0.01"); err != nil {
		t.Fatal(err)
	}
	out := ethRPC(t, srv, "txpool_inspect")
	if out["error"] != nil {
		t.Fatalf("txpool_inspect 失敗: %v", out["error"])
	}
	res, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("result 非 map: %T", out["result"])
	}
	pending, ok := res["pending"].(map[string]any)
	if !ok {
		t.Fatalf("pending 非 map: %T", res["pending"])
	}
	if len(pending) == 0 {
		t.Fatal("pending 應含一筆交易")
	}
	entry, ok := pending["tx01alice"].(map[string]any)
	if !ok {
		t.Fatalf("from 分組缺失: %v", pending)
	}
	nonceMap := entry["0x0"].(map[string]any)
	if nonceMap["hash"] != "v53pendingtx1" {
		t.Fatalf("hash 不符: %v", nonceMap["hash"])
	}
}

func TestEthGetBlockByNumberTags(t *testing.T) {
	n, srv := newTestRPCServer(t)
	waitHeight53(t, n, 2)

	// safe → 同 latest
	latest := ethRPC(t, srv, "eth_getBlockByNumber", "latest", false)
	safe := ethRPC(t, srv, "eth_getBlockByNumber", "safe", false)
	if latest["error"] != nil || safe["error"] != nil {
		t.Fatalf("latest/safe 失敗: %v %v", latest["error"], safe["error"])
	}
	lb := latest["result"].(map[string]any)
	sb := safe["result"].(map[string]any)
	if lb["number"] != sb["number"] {
		t.Fatalf("safe 應等於 latest: %v vs %v", lb["number"], sb["number"])
	}
	// finalized → FinalizedHeight>0 用高度；否則同 latest（不報錯）
	finalized := ethRPC(t, srv, "eth_getBlockByNumber", "finalized", false)
	if finalized["error"] != nil {
		t.Fatalf("finalized 失敗: %v", finalized["error"])
	}
	fb := finalized["result"].(map[string]any)
	if fb == nil {
		t.Fatal("finalized 回 nil")
	}
}

// strconv53 高度轉 16 進位字串（避免測試檔額外 import）。
func strconv53(h int64) string {
	return new(big.Int).SetInt64(h).Text(16)
}

