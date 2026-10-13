package node

import (
	"testing"
	"time"

	"tacm/internal/crypto"
)

// TestM122MultiPeerFallback 驗證 M122 多 peer 容錯：
// C 同時接入 A、B 兩錨點；最佳 peer（A）離線後，C 自動 fallback 次佳 peer（B）
// 並追齊（必要時走 reorg 切換到 B 鏈）。
func TestM122MultiPeerFallback(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	alice := mustAddr(t, kp)

	a := startFullNode(t, "a-m122", nil, false, alice)
	defer a.srv.Close()
	b := startFullNode(t, "b-m122", nil, false, alice)
	defer b.srv.Close()

	// A 出 8 塊（最佳），B 出 6 塊（次佳）。
	waitHeight(t, a.n, 8, 20*time.Second)
	waitHeight(t, b.n, 6, 20*time.Second)

	c := startFullNode(t, "c-m122", []string{a.url, b.url}, true, alice)
	defer c.srv.Close()
	// C 先追最佳 peer A 到 8。
	waitHeight(t, c.n, 8, 40*time.Second)

	// A 離線；B 續出到 10。C 應自動切換到 B（fallback）並追齊。
	a.srv.Close()
	waitHeight(t, b.n, 10, 20*time.Second)
	waitHeight(t, c.n, 10, 40*time.Second)

	// C 與 B 同高同哈希（同一鏈視角）。
	cb, _ := c.n.db.GetBlock(10)
	bb, _ := b.n.db.GetBlock(10)
	if cb == nil || bb == nil {
		t.Fatal("缺少高度 10 區塊")
	}
	if cb.Hash != bb.Hash {
		t.Fatalf("fallback 後高度 10 哈希不一致: %s vs %s", cb.Hash, bb.Hash)
	}
}
