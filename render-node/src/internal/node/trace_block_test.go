package node

// trace_block 參數變體測試：驗證 trace_block 同時接受區塊號碼與 0x 區塊哈希
// （Parity/viem traceBlock 支援 hash 或 height）；垃圾參數應回錯。
// 复用既有 ethRPC helper（POST /eth JSON-RPC）。

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTraceBlockByHashAndNumber(t *testing.T) {
	n, err := New(testCfg(t), "trc", 1)
	if err != nil {
		t.Fatal(err)
	}
	insertGenesis(t, n)
	for i := 0; i < 3; i++ {
		if err := n.produceBlock(); err != nil {
			t.Fatalf("手動出塊 %d 失敗: %v", i+1, err)
		}
	}
	defer func() { _ = n.Close() }()

	srv := httptest.NewServer(NewRPCServer(n).Handler())
	defer srv.Close()

	b1, err := n.db.GetBlock(1)
	if err != nil || b1 == nil {
		t.Fatal("缺少高度 1 區塊")
	}

	// trace_block by 0x 哈希 → 應回陣列（可能為空，因該塊無交易）。
	out := ethRPC(t, srv, "trace_block", "0x"+b1.Hash)
	if out["error"] != nil {
		t.Fatalf("trace_block(by-hash) 回錯: %v", out["error"])
	}
	if _, ok := out["result"].([]any); !ok {
		t.Fatalf("trace_block(by-hash) 應回 array，got %+v", out["result"])
	}

	// trace_block by 高度字串 → 正常。
	out2 := ethRPC(t, srv, "trace_block", "1")
	if out2["error"] != nil {
		t.Fatalf("trace_block(by-number) 回錯: %v", out2["error"])
	}

	// 格式合法但不存在的 0x 哈希 → 應回錯（區塊不存在）。
	out3 := ethRPC(t, srv, "trace_block", "0x"+strings.Repeat("ff", 32))
	if out3["error"] == nil {
		t.Fatal("不存在的區塊哈希應回錯")
	}
}
