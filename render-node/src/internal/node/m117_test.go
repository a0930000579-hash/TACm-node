package node

// M117：交易提交路徑並行驗證（Goroutine）＋JSON-RPC 2.0 batch 支援。
// 測試：批量提交正確性、並行 vs 串行效能對比、batch RPC e2e。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestM117BatchSubmit 批量提交 500 筆（並行驗簽）→ 完整出塊 → 餘額正確。
func TestM117BatchSubmit(t *testing.T) {
	n := benchNodeT(t, 1)
	pool, _ := n.CommunityPoolAddress()
	before := n.db.GetBalance(pool)
	txs := signBenchTxs(t, n, 500)

	hashes, errs := n.SubmitTransactions(txs)
	for i, e := range errs {
		if e != nil {
			t.Fatalf("批量提交第 %d 筆失敗: %v", i, e)
		}
		if hashes[i] == "" {
			t.Fatalf("批量提交第 %d 筆 hash 為空", i)
		}
	}
	if err := n.produceBlock(); err != nil {
		t.Fatalf("produce: %v", err)
	}
	blk, err := n.db.GetLatestBlock()
	if err != nil || blk == nil {
		t.Fatalf("latest block: %v", err)
	}
	if blk.TxCount < 500 {
		t.Fatalf("區塊應含 500 筆交易，得 %d", blk.TxCount)
	}
	after := n.db.GetBalance(pool)
	spent := parseTACM(before) - parseTACM(after)
	if spent < 500 || spent > 520 {
		t.Fatalf("pool 支出異常: before %s after %s", before, after)
	}
	t.Logf("M117 批量冒煙通過：%d 筆提交全成功，區塊 %d 筆，支出 %v TACm", len(hashes), blk.TxCount, spent)
}

// TestM117BatchMixed 批量中含壞簽名：壞筆報錯、其餘正常。
func TestM117BatchMixed(t *testing.T) {
	n := benchNodeT(t, 1)
	txs := signBenchTxs(t, n, 10)
	// 竄改最後一筆簽名（不影響前方 nonce 連續提交）。
	txs[9]["signature"] = strings.Repeat("ab", 64)

	hashes, errs := n.SubmitTransactions(txs)
	for i := range errs {
		if i == 9 {
			if errs[i] == nil {
				t.Fatalf("第 9 筆應驗簽失敗")
			}
			continue
		}
		if errs[i] != nil {
			t.Fatalf("第 %d 筆不應失敗: %v", i, errs[i])
		}
		if hashes[i] == "" {
			t.Fatalf("第 %d 筆 hash 為空", i)
		}
	}
}

// BenchmarkBatchSubmit 對比：500 筆並行批量 vs 逐筆串行提交。
func BenchmarkBatchSubmit(b *testing.B) {
	n := benchNodeB(b, 1)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		txs := signBenchTxs(b, n, 500)
		hs, es := n.SubmitTransactions(txs)
		for _, e := range es {
			if e != nil {
				b.Fatalf("batch: %v", e)
			}
		}
		_ = hs
	}
}

// BenchmarkSerialSubmit 對照組：逐筆 SubmitTransaction（串行驗簽）。
func BenchmarkSerialSubmit(b *testing.B) {
	n := benchNodeB(b, 1)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		txs := signBenchTxs(b, n, 500)
		for _, tx := range txs {
			if _, err := n.SubmitTransaction(tx); err != nil {
				b.Fatalf("serial: %v", err)
			}
		}
	}
}

// TestM117JSONRPCBatch batch RPC e2e：POST /eth 陣列（net_version＋eth_blockNumber）→ 陣列回應。
func TestM117JSONRPCBatch(t *testing.T) {
	n := benchNodeT(t, 1)
	srv := httptest.NewServer(NewRPCServer(n).Handler())
	defer srv.Close()

	body := `[{"jsonrpc":"2.0","id":1,"method":"net_version","params":[]},{"jsonrpc":"2.0","id":2,"method":"eth_blockNumber","params":[]}]`
	resp, err := http.Post(srv.URL+"/eth", "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	var out []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("batch 回應解析: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("batch 回應應 2 筆，得 %d", len(out))
	}
	for i, r := range out {
		if r["jsonrpc"] != "2.0" {
			t.Fatalf("第 %d 筆 jsonrpc 不符: %v", i, r)
		}
		if r["error"] != nil {
			t.Fatalf("第 %d 筆錯誤: %v", i, r["error"])
		}
	}
	t.Logf("M117 batch RPC 通過：回應 %v", out)
}

// TestM117JSONRPCBatchSingle 單一請求路徑不受 batch 改造影響。
func TestM117JSONRPCBatchSingle(t *testing.T) {
	n := benchNodeT(t, 1)
	srv := httptest.NewServer(NewRPCServer(n).Handler())
	defer srv.Close()

	body := `{"jsonrpc":"2.0","id":7,"method":"net_version","params":[]}`
	resp, err := http.Post(srv.URL+"/eth", "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("回應解析: %v", err)
	}
	if out["id"] != nil && out["error"] != nil {
		t.Fatalf("單一請求不應錯誤: %v", out)
	}
	_ = time.Now()
}
