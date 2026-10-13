package node

import (
	"math"
	"testing"
	"time"

	"tacm/internal/crypto"
)

// TestM121LateSyncWithTxs 驗證 M121 分片並行拉取後的晚加入同步：
// A 錨點出塊期間含一筆轉帳；D 晚加入後快速追齊，交易與區塊哈希與錨點一致。
func TestM121LateSyncWithTxs(t *testing.T) {
	aliceKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	alice := mustAddr(t, aliceKP)
	bobKP, _ := crypto.GenerateKeyPair()
	bob := mustAddr(t, bobKP)

	a := startFullNode(t, "a-m121", nil, false, alice)
	defer a.srv.Close()

	// 錨點先出一筆交易並持續出塊。
	if _, err := a.n.SubmitTransaction(signedTx(t, aliceKP, bob, "7.5", "0.01", 0)); err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitHeight(t, a.n, 8, 20*time.Second)

	// D 晚加入（缺口 ≥8 塊，經 syncOnce 追齊）。
	d := startFullNode(t, "d-m121", []string{a.url}, true, alice)
	defer d.srv.Close()
	waitHeight(t, d.n, 8, 30*time.Second)

	// 交易已同步：bob 餘額在 D 視角為 7.5。
	if got := balanceOf(t, d.n, bob); math.Abs(got-7.5) > 1e-6 {
		t.Fatalf("D bob balance = %g, want 7.5", got)
	}
	// 同步後同一高度區塊哈希一致。
	ab, _ := a.n.db.GetBlock(8)
	db2, _ := d.n.db.GetBlock(8)
	if ab == nil || db2 == nil {
		t.Fatal("缺少高度 8 區塊")
	}
	if ab.Hash != db2.Hash {
		t.Fatalf("高度 8 哈希不一致: %s vs %s", ab.Hash, db2.Hash)
	}
}
