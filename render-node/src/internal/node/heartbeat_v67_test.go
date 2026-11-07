package node

// heartbeat_v67_test.go — v67：node-hb 與 miner-hb 共用節點金鑰的 nonce 競爭修正驗證。
// node-hb 失敗後重新 buildHeartbeatTx（以最新 GetNonce+pendingTxCount 重建 nonce）
// 再提交——本測試驗證「pending nonce 被佔用後，重新提交仍以連續 nonce 成功」，
// 即重試機制依賴的核心前提成立。

import "testing"

func TestNodeHeartbeatNonceRebuild(t *testing.T) {
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
	defer func() { _ = n.Close() }()

	// 模擬 miner-hb 剛提交佔用 1 個 pending nonce（from=節點，同一金鑰）。
	n.pendingTxInc(admin)

	// 第一次提交：build 時 pending=1 → nonce=鏈上+1，應成功。
	h1, err := n.SubmitHeartbeat(admin, true, 0)
	if err != nil {
		t.Fatalf("佔用後首次提交應成功: %v", err)
	}
	if h1 == "" {
		t.Fatal("hash 不應為空")
	}
	// 第二次提交：pending=2 → nonce=鏈上+2（連續），應成功——等同 node-hb 重試路徑。
	h2, err := n.SubmitHeartbeat(admin, true, 0)
	if err != nil {
		t.Fatalf("連續重提交應成功（重試路徑）: %v", err)
	}
	if h2 == "" || h2 == h1 {
		t.Fatalf("兩筆交易哈希應不同且非空: %q %q", h1, h2)
	}
}
