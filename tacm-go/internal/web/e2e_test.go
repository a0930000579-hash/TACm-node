package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tacm/internal/chaindb"
	"tacm/internal/config"
	"tacm/internal/node"
)

// TestExplorerRealNode 以運行中的真實節點端到端驗證區塊瀏覽（非靜態、非替身）。
func TestExplorerRealNode(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.BlockTime = 1

	n, err := node.New(cfg, "webe2e", 1)
	if err != nil {
		t.Fatalf("New node: %v", err)
	}
	defer n.Close()

	now := time.Now().Unix()
	genesis := &chaindb.Block{
		Height: 0, Hash: strings.Repeat("0", 64),
		Proposer: "genesis", Ts: now,
	}
	if err := n.DB().InsertBlock(genesis, nil); err != nil {
		t.Fatalf("創世: %v", err)
	}
	n.Start()

	// 等待至少出 2 個塊。
	deadline := time.Now().Add(15 * time.Second)
	for n.DB().GetTipHeight() < 2 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if n.DB().GetTipHeight() < 2 {
		t.Fatalf("未如期出塊，height=%d", n.DB().GetTipHeight())
	}

	srv, err := New(NewNodeDataSource(n))
	if err != nil {
		t.Fatalf("New web: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// M80：`/` 為品牌門戶（landing）；系統主頁（含最新區塊）移至 /app。
	resp, _ := http.Get(ts.URL + "/")
	body := readAllString(resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("門戶 狀態=%d", resp.StatusCode)
	}
	if !strings.Contains(body, "TAC Ai Smart Chain") {
		t.Errorf("門戶缺少品牌標題")
	}
	resp.Body.Close()
	resp, _ = http.Get(ts.URL + "/app")
	body = readAllString(resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("系統主頁 狀態=%d", resp.StatusCode)
	}
	if !strings.Contains(body, "最新區塊") {
		t.Errorf("系統主頁缺少區塊列表區塊")
	}
	resp.Body.Close()

	// 區塊詳情頁。
	resp, _ = http.Get(ts.URL + "/block/1")
	body = readAllString(resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("區塊頁 狀態=%d", resp.StatusCode)
	}
	if !strings.Contains(body, "Merkle 根") {
		t.Errorf("區塊頁缺少 Merkle 字段")
	}
	resp.Body.Close()

	// 節點地址頁顯示 coinbase 累計餘額。
	resp, _ = http.Get(ts.URL + "/address/" + n.Address())
	body = readAllString(resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("地址頁 狀態=%d", resp.StatusCode)
	}
	if !strings.Contains(body, "可用餘額") {
		t.Errorf("地址頁缺少餘額")
	}
	resp.Body.Close()

	// 真實 coinbase 交易詳情頁。
	detail, err := n.GetBlockDetail(1)
	if err != nil || len(detail.Transactions) == 0 {
		t.Fatalf("區塊1 無交易: err=%v", err)
	}
	txHash := detail.Transactions[0].TxHash
	resp, _ = http.Get(ts.URL + "/tx/" + txHash)
	body = readAllString(resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("交易頁 狀態=%d", resp.StatusCode)
	}
	if !strings.Contains(body, txHash[:12]) {
		t.Errorf("交易頁未顯示真實交易哈希")
	}
	resp.Body.Close()

	// M62：公開瀏覽器頁——統計＋搜尋＋最新區塊/交易表。
	resp, _ = http.Get(ts.URL + "/explorer")
	body = readAllString(resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("瀏覽器頁 狀態=%d", resp.StatusCode)
	}
	for _, want := range []string{"鏈上搜尋", "最新區塊", "最新交易"} {
		if !strings.Contains(body, want) {
			t.Errorf("瀏覽器頁缺少 %q", want)
		}
	}
	resp.Body.Close()

	// M62：搜尋 API 真實鏈——區塊高度→block、節點地址→address、真實交易哈希→tx。
	check := func(q, want string) {
		resp, err := http.Get(ts.URL + "/api/search?q=" + q)
		if err != nil {
			t.Fatalf("搜尋 %q: %v", q, err)
		}
		b := readAllString(resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("搜尋 %q 狀態=%d", q, resp.StatusCode)
		}
		if !strings.Contains(b, want) {
			t.Errorf("搜尋 %q 未含 %q（body=%s）", q, want, b)
		}
		resp.Body.Close()
	}
	check("1", `"type":"block"`)
	check(n.Address(), `"type":"address"`)
	check(txHash, `"type":"tx"`)
	check("zzz_no_such", `"type":"none"`)

	// M63/M65：公開 seed 列表——真實節點下任何入口一致。
	resp, _ = http.Get(ts.URL + "/api/peers")
	body = readAllString(resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"chain_id":"tacm-mainnet-1"`) {
		t.Errorf("peers API 異常 狀態=%d", resp.StatusCode)
	}
	resp.Body.Close()

	// M66：鏈上審計 JSON——/api/block/{h} 回平鋪區塊欄位＋transactions（含 coinbase）。
	resp, err = http.Get(ts.URL + "/api/block/1")
	if err != nil {
		t.Fatalf("block API: %v", err)
	}
	body = readAllString(resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("block API 狀態=%d", resp.StatusCode)
	}
	for _, want := range []string{`"height":1`, `"transactions":[`, `"memo"`, `"coinbase:reserve"`, `"coinbase:pool"`} {
		if !strings.Contains(body, want) {
			t.Errorf("/api/block/1 缺少 %q（body=%s）", want, body)
		}
	}
	resp.Body.Close()
	// 不存在的高度 → 404（審計器以 404 判定區塊缺失）。
	resp, _ = http.Get(ts.URL + "/api/block/999999")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("/api/block/999999 應 404，實際 %d", resp.StatusCode)
	}
	resp.Body.Close()

	// M67/M71：公開鏈上審計——真實節點驗證最近塊 coinbase 不變式＋全鏈賬本重放，結果必須一致。
	resp, err = http.Get(ts.URL + "/api/audit?blocks=10")
	if err != nil {
		t.Fatalf("audit API: %v", err)
	}
	body = readAllString(resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("audit API 狀態=%d", resp.StatusCode)
	}
	for _, want := range []string{`"chain_id":"tacm-mainnet-1"`, `"consistent":true`, `"coinbase_fail":0`, `"structure_ok":true`, `"total_mined_tacm"`, `"ledger_ok":true`, `"tx_checked"`, `"tx_sig_pass"`, `"tx_nonce_pass"`, `"tx_balance_pass"`} {
		if !strings.Contains(body, want) {
			t.Errorf("/api/audit 缺少 %q（body=%s）", want, body)
		}
	}
	resp.Body.Close()
}
