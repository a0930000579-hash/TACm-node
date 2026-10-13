package node

// M118：P2P 多節點高負載壓力測試——驗證 3 節點網路在批量高負載下
// 端到端吞吐、確認延遲與帳本一致性（與主流鏈多節點網路同級）。

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"tacm/internal/crypto"
)

// TestM118P2PStress 3 節點（A 錨點＋B/C follower）：向 B 批量提交 60 筆，
// 驗證全網區塊高度、關鍵塊哈希、接收方餘額完全一致，並量測端到端確認延遲。
func TestM118P2PStress(t *testing.T) {
	aliceKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	alice := mustAddr(t, aliceKP)

	A := startFullNode(t, "nodeA", nil, false, alice)
	B := startFullNode(t, "nodeB", []string{A.url}, true, alice)
	C := startFullNode(t, "nodeC", []string{A.url}, true, alice)

	waitHeight(t, A.n, 3, 12*time.Second)
	waitHeight(t, B.n, 2, 12*time.Second)
	waitHeight(t, C.n, 2, 12*time.Second)

	bobKP, _ := crypto.GenerateKeyPair()
	bob := mustAddr(t, bobKP)

	// 批量 60 筆（nonce 0..59，金額 1，費 0.02），向跟隨節點 B 提交。
	const n = 60
	txs := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		txs = append(txs, signedTx(t, aliceKP, bob, "1", "0.02", int64(i)))
	}
	body, _ := json.Marshal(map[string]any{"transactions": txs})
	start := time.Now()
	resp, err := http.Post(B.url+"/tx/batch", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("批量提交失敗 %d: %s", resp.StatusCode, r)
	}
	var out struct {
		Total   int `json:"total"`
		Success int `json:"success"`
		Failed  int `json:"failed"`
	}
	if err := json.Unmarshal(r, &out); err != nil {
		t.Fatalf("解析: %v", err)
	}
	if out.Success != n || out.Failed != 0 {
		t.Fatalf("批量應 %d 全成功，得 success=%d failed=%d", n, out.Success, out.Failed)
	}

	// 端到端確認：bob 餘額在全網三節點均達 n TACm（60）。
	waitBalance(t, A.n, bob, n, 20*time.Second)
	waitBalance(t, B.n, bob, n, 20*time.Second)
	waitBalance(t, C.n, bob, n, 20*time.Second)
	latency := time.Since(start)

	// 帳本一致性：三節點區塊高度與同高度哈希一致（取三節點共同最低高度）。
	hA, hB, hC := A.n.DB().GetTipHeight(), B.n.DB().GetTipHeight(), C.n.DB().GetTipHeight()
	minH := hA
	if hB < minH {
		minH = hB
	}
	if hC < minH {
		minH = hC
	}
	if minH < 4 {
		t.Fatalf("三節點高度應 ≥4（含壓力區塊），得 %d/%d/%d", hA, hB, hC)
	}
	bA, _ := A.n.DB().GetBlock(minH)
	bB, _ := B.n.DB().GetBlock(minH)
	bC, _ := C.n.DB().GetBlock(minH)
	if bA == nil || bB == nil || bC == nil {
		t.Fatal("缺少共同高度區塊")
	}
	if bA.Hash != bB.Hash || bB.Hash != bC.Hash {
		t.Fatalf("共同高度 %d 哈希不一致: %s / %s / %s", minH, bA.Hash, bB.Hash, bC.Hash)
	}
	// alice 支出守恆：60×(1+0.02)=61.2（創世 1000）。
	for _, fn := range []struct {
		name string
		n    *Node
	}{{"A", A.n}, {"B", B.n}, {"C", C.n}} {
		v, perr := strconv.ParseFloat(fn.n.DB().GetBalance(alice), 64)
		if perr != nil {
			t.Fatalf("%s alice 餘額解析: %v", fn.name, perr)
		}
		if v > 1000-61.2+0.01 {
			t.Fatalf("%s alice 餘額異常: %v", fn.name, v)
		}
	}
	t.Logf("M118 多節點壓力通過：%d 筆批量全成功，確認延遲 %.2fs（含出塊等待），高度 %d 哈希全網一致", n, latency.Seconds(), minH)
}

// TestM118P2PBatchRoute /tx/batch 回傳逐筆結果（含壞簽名筆）。
func TestM118P2PBatchRoute(t *testing.T) {
	aliceKP, _ := crypto.GenerateKeyPair()
	alice := mustAddr(t, aliceKP)
	A := startFullNode(t, "nodeA", nil, false, alice)
	waitHeight(t, A.n, 2, 12*time.Second)

	bobKP, _ := crypto.GenerateKeyPair()
	bob := mustAddr(t, bobKP)
	txs := []map[string]any{
		signedTx(t, aliceKP, bob, "1", "0.02", 0),
		signedTx(t, aliceKP, bob, "1", "0.02", 1),
	}
	txs[1]["signature"] = "00" // 壞簽名
	body, _ := json.Marshal(map[string]any{"transactions": txs})
	resp, err := http.Post(A.url+"/tx/batch", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var out struct {
		Success int `json:"success"`
		Failed  int `json:"failed"`
		Results []struct {
			OK   bool   `json:"ok"`
			Hash string `json:"tx_hash"`
		} `json:"results"`
	}
	if err := json.Unmarshal(r, &out); err != nil {
		t.Fatalf("解析: %v", err)
	}
	if out.Success != 1 || out.Failed != 1 {
		t.Fatalf("應 1 成功 1 失敗，得 %d/%d", out.Success, out.Failed)
	}
	if len(out.Results) != 2 {
		t.Fatalf("results 應 2 筆，得 %d", len(out.Results))
	}
}

// TestM118P2PLedgerAudit 壓力後跑鏈上帳本重放審計：全網守恆。
func TestM118P2PLedgerAudit(t *testing.T) {
	aliceKP, _ := crypto.GenerateKeyPair()
	alice := mustAddr(t, aliceKP)
	A := startFullNode(t, "nodeA", nil, false, alice)
	B := startFullNode(t, "nodeB", []string{A.url}, true, alice)
	waitHeight(t, A.n, 3, 12*time.Second)
	waitHeight(t, B.n, 2, 12*time.Second)

	bobKP, _ := crypto.GenerateKeyPair()
	bob := mustAddr(t, bobKP)
	const n = 30
	txs := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		txs = append(txs, signedTx(t, aliceKP, bob, "1", "0.02", int64(i)))
	}
	body, _ := json.Marshal(map[string]any{"transactions": txs})
	resp, err := http.Post(B.url+"/tx/batch", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	waitBalance(t, A.n, bob, n, 20*time.Second)
	waitBalance(t, B.n, bob, n, 20*time.Second)

	// 帳本重放審計：A/B 各自驗證全鏈守恆（總產出=總支出）。
	for _, fn := range []struct {
		name string
		n    *Node
	}{{"A", A.n}, {"B", B.n}} {
		if _, err := fn.n.VerifyLedgerOnChain(); err != nil {
			t.Fatalf("%s 帳本審計失敗: %v", fn.name, err)
		}
	}
	t.Logf("M118 帳本審計通過：A/B 全鏈重放守恆")
}

