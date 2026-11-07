package tacclient

import (
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"tacm/internal/chaindb"
	"tacm/internal/config"
	"tacm/internal/node"
)

// TestTransferEndToEnd 以真實節點驗證 SDK 完整閉環：
// 創世分配 → 查 nonce → 簽署 → 提交 → 出塊 → 餘額變動。
func TestTransferEndToEnd(t *testing.T) {
	alice, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	bob, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.BlockTime = 1
	n, err := node.New(cfg, "sdke2e", 1)
	if err != nil {
		t.Fatalf("node.New: %v", err)
	}
	defer n.Close()

	now := time.Now().Unix()
	genesis := &chaindb.Block{
		Height: 0, Hash: strings.Repeat("0", 64),
		Proposer: "genesis", Ts: now,
	}
	if err := n.DB().InsertBlock(genesis, []chaindb.Transaction{{
		TxHash: "sdkalloc", ToAddr: alice.Address(), Amount: "1000", Fee: "0", Ts: now,
	}}); err != nil {
		t.Fatalf("創世分配: %v", err)
	}
	n.Start()

	ts := httptest.NewServer(node.NewRPCServer(n).Handler())
	defer ts.Close()
	c := NewClient(ts.URL)

	if bal, _ := c.Balance(alice.Address()); bal != "1000" && bal != "1000.0" {
		t.Fatalf("alice 初始餘額=%s", bal)
	}

	hash, err := c.Transfer(alice, bob.Address(), "5", "0.1", "sdk-e2e")
	if err != nil {
		t.Fatalf("Transfer: %v", err)
	}
	if hash == "" {
		t.Fatal("空交易哈希")
	}

	// 等待出塊且 bob 到帳。
	deadline := time.Now().Add(15 * time.Second)
	for {
		bal, _ := c.Balance(bob.Address())
		if bal != "0" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("超時未到帳（tx=%s）", hash)
		}
		time.Sleep(100 * time.Millisecond)
	}

	bobBal, _ := strconv.ParseFloat(mustBalance(t, c, bob.Address()), 64)
	if bobBal != 5 {
		t.Fatalf("bob 餘額=%g want 5", bobBal)
	}
	aliceBal, _ := strconv.ParseFloat(mustBalance(t, c, alice.Address()), 64)
	if aliceBal != 994.9 {
		t.Fatalf("alice 餘額=%g want 994.9", aliceBal)
	}

	// 交易已入鏈且可查。
	tx, err := c.Transaction(hash)
	if err != nil || tx == nil {
		t.Fatalf("查交易 %s: %v", hash, err)
	}
	if tx.FromAddr != alice.Address() || tx.Amount != "5" {
		t.Fatalf("交易欄位異常: %+v", tx)
	}
}

func mustBalance(t *testing.T, c *Client, addr string) string {
	t.Helper()
	bal, err := c.Balance(addr)
	if err != nil {
		t.Fatalf("查餘額 %s: %v", addr, err)
	}
	return bal
}
