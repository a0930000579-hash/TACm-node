package node

import (
	"net/http/httptest"
	"testing"
	"time"

	"tacm/internal/config"
	"tacm/internal/spv"
)

func startBridgeNode(t *testing.T) (*Node, *httptest.Server) {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.BlockTime = 1
	cfg.EnableBridge = true
	n, err := New(cfg, "bridgenode", 2)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewRPCServer(n).Handler())
	n.Start()
	return n, srv
}

func waitTipAtLeast(t *testing.T, srv *httptest.Server, want int64) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		_, st := httpJSON(t, "GET", srv.URL+"/status", nil)
		if int64(numField(st["block_height"])) >= want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("鏈頂未達 %d", want)
}

func TestBridgeHTTPEndToEnd(t *testing.T) {
	n, srv := startBridgeNode(t)
	defer srv.Close()
	defer n.Close()

	sc, chains := httpJSON(t, "GET", srv.URL+"/bridge/chains", nil)
	if sc != 200 || len(chains["chains"].(map[string]any)) != 6 {
		t.Fatalf("鏈列表錯誤: %d %v", sc, chains)
	}

	// Lock & Mint 全流程。
	sc, lock := httpJSON(t, "POST", srv.URL+"/bridge/lock", map[string]any{
		"source_chain": "tacm", "target_chain": "ethereum",
		"source_address": "tx0src", "target_address": "0xtgt",
		"amount": 100, "token": "TACM",
	})
	if sc != 200 {
		t.Fatalf("lock: %d %v", sc, lock)
	}
	id := lock["bridge_tx_id"].(string)

	sc, cl := httpJSON(t, "POST", srv.URL+"/bridge/confirm-lock",
		map[string]any{"bridge_tx_id": id, "tx_hash": "lockhash"})
	if sc != 200 || cl["status"] != "locked" {
		t.Fatalf("confirm-lock: %d %v", sc, cl)
	}
	sc, minted := httpJSON(t, "POST", srv.URL+"/bridge/mint",
		map[string]any{"bridge_tx_id": id, "tx_hash": "minthash"})
	if sc != 200 || minted["status"] != "confirmed" {
		t.Fatalf("mint: %d %v", sc, minted)
	}

	// Burn & Unlock 全流程。
	_, burn := httpJSON(t, "POST", srv.URL+"/bridge/burn", map[string]any{
		"source_chain": "ethereum", "target_chain": "tacm",
		"source_address": "0xsrc", "target_address": "tx0tgt",
		"amount": 50, "token": "TACM",
	})
	id2 := burn["bridge_tx_id"].(string)
	sc, cb := httpJSON(t, "POST", srv.URL+"/bridge/confirm-burn",
		map[string]any{"bridge_tx_id": id2, "tx_hash": "burnhash"})
	if sc != 200 || cb["status"] != "burning" {
		t.Fatalf("confirm-burn: %d %v", sc, cb)
	}
	sc, unlocked := httpJSON(t, "POST", srv.URL+"/bridge/unlock",
		map[string]any{"bridge_tx_id": id2, "tx_hash": "unlockhash"})
	if sc != 200 || unlocked["status"] != "confirmed" {
		t.Fatalf("unlock: %d %v", sc, unlocked)
	}

	sc, _ = httpJSON(t, "GET", srv.URL+"/bridge/stats", nil)
	if sc != 200 {
		t.Fatalf("stats: %d", sc)
	}

	// 不支持鏈 → 400。
	sc, _ = httpJSON(t, "POST", srv.URL+"/bridge/lock", map[string]any{
		"source_chain": "tacm", "target_chain": "solana",
		"source_address": "a", "target_address": "b",
		"amount": 10, "token": "TACM",
	})
	if sc != 400 {
		t.Fatalf("不支持鏈應 400, got %d", sc)
	}
}

func TestLightClientEndToEnd(t *testing.T) {
	n, srv := startBridgeNode(t)
	defer srv.Close()
	defer n.Close()

	waitTipAtLeast(t, srv, 2)

	lc := spv.NewLightClient(srv.URL, 100)
	if ok, err := lc.LoadValidators(); !ok || err != nil {
		t.Fatalf("載入驗證人: %v %v", ok, err)
	}
	sync, err := lc.SyncHeaders(50)
	if err != nil || !sync.ChainOK {
		t.Fatalf("同步區塊頭: %v %+v", err, sync)
	}

	// 驗證 height1 的 coinbase 交易（已標準化進 Merkle）。
	_, blk := httpJSON(t, "GET", srv.URL+"/block/1", nil)
	txs, ok := blk["transactions"].([]any)
	if !ok || len(txs) == 0 {
		t.Fatalf("height1 無交易: %v", blk)
	}
	txHash := txs[0].(map[string]any)["tx_hash"].(string)

	res, err := lc.VerifyTransaction(txHash)
	if err != nil {
		t.Fatalf("驗證交易出錯: %v", err)
	}
	if !res.Included || res.Height != 1 || res.Confirmations < 1 {
		t.Fatalf("交易驗證結果錯誤: %+v", res)
	}

	fin, err := lc.VerifyFinality(1)
	if err != nil {
		t.Fatalf("驗證最終性出錯: %v", err)
	}
	t.Logf("height1 finalized=%v power=%d quorum=%d",
		fin.Finalized, fin.Power, fin.Quorum)

	// 不存在的交易不應被判為包含。
	missing, err := lc.VerifyTransaction("deadbeefdeadbeef")
	if err != nil {
		t.Fatal(err)
	}
	if missing.Included {
		t.Fatal("不存在交易不應 included")
	}
}
