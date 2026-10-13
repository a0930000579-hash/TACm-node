package node

import (
	"math"
	"strconv"
	"testing"
	"time"

	"tacm/internal/crypto"
)

// TestM120BatchGossipE2E 驗證批量提交（SubmitTransactions）＋批量廣播全網一致：
// 60 筆批發經 B → 全網 C 收到同高同哈希，且 bob 餘額 60。
func TestM120BatchGossipE2E(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	alice := mustAddr(t, kp)
	bobKP, _ := crypto.GenerateKeyPair()
	bob := mustAddr(t, bobKP)

	a := startFullNode(t, "a-m120e2e", nil, false, alice)
	defer a.srv.Close()
	b := startFullNode(t, "b-m120e2e", []string{a.url}, true, alice)
	defer b.srv.Close()
	c := startFullNode(t, "c-m120e2e", []string{b.url}, true, alice)
	defer c.srv.Close()
	waitHeight(t, b.n, 2, 12*time.Second)
	waitHeight(t, c.n, 2, 12*time.Second)
	time.Sleep(3 * time.Second) // 確保 B→A 心跳完成（onlinePeers 收錄 A）

	txs := make([]map[string]any, 0, 60)
	for i := 0; i < 60; i++ {
		txs = append(txs, signedTx(t, kp, bob, "1", "0.01", int64(i)))
	}
	hashes, errs := b.n.SubmitTransactions(txs)
	for i, e := range errs {
		if e != nil {
			t.Fatalf("tx[%d] submit error: %v", i, e)
		}
	}
	if len(hashes) != 60 {
		t.Fatalf("hashes = %d, want 60", len(hashes))
	}

	// 全網收斂：bob 餘額 60 且 A/C 同高度同哈希（批量 txs 傳播未失序、未丟失）。
	waitBalance(t, c.n, bob, 60, 30*time.Second)
	if got := balanceOf(t, a.n, bob); math.Abs(got-60) > 1e-6 {
		t.Fatalf("A bob balance = %g, want 60", got)
	}
	hA := a.n.db.GetTipHeight()
	hC := c.n.db.GetTipHeight()
	if hA != hC {
		t.Fatalf("height mismatch A=%d C=%d", hA, hC)
	}
	blkA, errA := a.n.db.GetLatestBlock()
	blkC, errC := c.n.db.GetLatestBlock()
	if errA != nil || errC != nil {
		t.Fatalf("latest block: A err=%v C err=%v", errA, errC)
	}
	if blkA.Hash != blkC.Hash {
		t.Fatalf("tip hash mismatch A=%s C=%s", blkA.Hash, blkC.Hash)
	}
}

// balanceOf 讀取帳戶餘額（測試輔助）。
func balanceOf(t *testing.T, n *Node, addr string) float64 {
	t.Helper()
	bal, err := strconv.ParseFloat(n.db.GetBalance(addr), 64)
	if err != nil {
		t.Fatalf("parse balance %q: %v", n.db.GetBalance(addr), err)
	}
	return bal
}
