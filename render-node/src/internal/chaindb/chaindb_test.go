package chaindb

import (
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"tacm/internal/crypto"
)

func openTmpDB(t *testing.T) *ChainDB {
	t.Helper()
	c, err := Open(filepath.Join(t.TempDir(), "chain.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func newAddr(t *testing.T) string {
	t.Helper()
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	addr, err := kp.Address()
	if err != nil {
		t.Fatal(err)
	}
	return addr
}

func TestBlockReward(t *testing.T) {
	cases := []struct {
		h    int64
		want float64
	}{
		{0, 0}, {1, 10}, {4999999, 10}, {5000000, 5},
		{10000000, 2.5}, {15000000, 1.25},
	}
	for _, c := range cases {
		if got := BlockReward(c.h); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("BlockReward(%d)=%g want %g", c.h, got, c.want)
		}
	}
}

// genesisWithAlloc 寫入創世塊，並通過創世分配交易給 alice 入賬 1000。
func genesisWithAlloc(t *testing.T, c *ChainDB, alice string) int64 {
	t.Helper()
	now := time.Now().Unix()
	genTx := Transaction{
		TxHash: "genalloc", ToAddr: alice, Amount: "1000",
		Fee: "0", Ts: now,
	}
	genesis := &Block{
		Height: 0, Hash: strings.Repeat("0", 64),
		MerkleRoot: "gmr", Proposer: "genesis", Ts: now, TxCount: 1,
	}
	if err := c.InsertBlock(genesis, []Transaction{genTx}); err != nil {
		t.Fatalf("寫入創世塊失敗: %v", err)
	}
	return now
}

func assertBalance(t *testing.T, c *ChainDB, addr string, want float64) {
	t.Helper()
	got, err := strconv.ParseFloat(c.GetBalance(addr), 64)
	if err != nil {
		t.Fatalf("解析餘額失敗 %s: %v", addr, err)
	}
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s 餘額 = %g, want %g", addr, got, want)
	}
}

func TestStateTransition(t *testing.T) {
	c := openTmpDB(t)
	alice, bob, proposer := newAddr(t), newAddr(t), newAddr(t)
	now := genesisWithAlloc(t, c, alice)

	// height1：coinbase 增發 10 + alice → bob 100，fee 2。
	zeroHash := strings.Repeat("0", 64)
	coinbase := BuildCoinbaseTx(1, proposer, now)
	tx := Transaction{
		TxHash: "tx1", FromAddr: alice, ToAddr: bob,
		Amount: "100", Fee: "2", Nonce: 0, Ts: now,
	}
	block := &Block{
		Height: 1, Hash: "b1", PrevHash: &zeroHash, MerkleRoot: "mr",
		Proposer: "proposer", ProposerAddress: proposer,
		Ts: now, TxCount: 2, Difficulty: 1, Nonce: 5,
	}
	if err := c.InsertBlock(block, []Transaction{coinbase, tx}); err != nil {
		t.Fatalf("插入 height1 失敗: %v", err)
	}

	assertBalance(t, c, alice, 898)   // 1000 - 100 - 2
	assertBalance(t, c, bob, 100)     // 收款
	assertBalance(t, c, proposer, 12) // coinbase 10 + fee 2

	// 總量 = 初始 1000 + coinbase 10 = 1010。
	total, _ := strconv.ParseFloat(c.GetBalance(alice), 64)
	for _, a := range []string{bob, proposer} {
		v, _ := strconv.ParseFloat(c.GetBalance(a), 64)
		total += v
	}
	if math.Abs(total-1010) > 1e-9 {
		t.Errorf("總量 = %g, want 1010", total)
	}
	if got := c.GetNonce(alice); got != 1 {
		t.Errorf("alice nonce = %d, want 1", got)
	}
}

func TestQueries(t *testing.T) {
	c := openTmpDB(t)
	alice := newAddr(t)
	now := genesisWithAlloc(t, c, alice)

	b, err := c.GetBlock(0)
	if err != nil || b == nil {
		t.Fatalf("查創世塊失敗: %v", err)
	}
	if b.Hash != strings.Repeat("0", 64) {
		t.Errorf("創世哈希錯誤: %s", b.Hash)
	}
	if got := c.GetTipHeight(); got != 0 {
		t.Errorf("tip=%d want 0", got)
	}

	zeroHash := strings.Repeat("0", 64)
	cb := BuildCoinbaseTx(1, alice, now)
	blk := &Block{
		Height: 1, Hash: "b1", PrevHash: &zeroHash, Proposer: "p",
		ProposerAddress: alice, Ts: now, TxCount: 1, Difficulty: 1,
	}
	if err := c.InsertBlock(blk, []Transaction{cb}); err != nil {
		t.Fatal(err)
	}
	if cnt, _ := c.GetBlockCount(); cnt != 2 {
		t.Errorf("block count=%d want 2", cnt)
	}
	if got := c.GetTipHeight(); got != 1 {
		t.Errorf("tip=%d want 1", got)
	}
	if byHash, _ := c.GetBlockByHash("b1"); byHash == nil || byHash.Height != 1 {
		t.Errorf("按哈希查塊失敗")
	}
	if missing, _ := c.GetBlock(99); missing != nil {
		t.Errorf("height99 應為 nil")
	}
	blocks, err := c.GetBlocks(10, 0)
	if err != nil || len(blocks) != 2 || blocks[0].Height != 1 {
		t.Errorf("GetBlocks 異常: %v %d", err, len(blocks))
	}
	acc, err := c.GetAccount(alice)
	if err != nil || acc == nil {
		t.Fatalf("查賬戶失敗: %v", err)
	}
	if acc.Balance != "1010.0" {
		t.Errorf("alice 賬戶餘額=%s want 1010.0（1000+coinbase10）", acc.Balance)
	}
}

func TestTransactionQueries(t *testing.T) {
	c := openTmpDB(t)
	alice, bob := newAddr(t), newAddr(t)
	now := genesisWithAlloc(t, c, alice)

	zeroHash := strings.Repeat("0", 64)
	tx := Transaction{
		TxHash: "txq1", FromAddr: alice, ToAddr: bob,
		Amount: "50", Fee: "1", Nonce: 0, Ts: now,
	}
	blk := &Block{
		Height: 1, Hash: "bq1", PrevHash: &zeroHash, Proposer: "p",
		ProposerAddress: alice, Ts: now, TxCount: 1, Difficulty: 1,
	}
	if err := c.InsertBlock(blk, []Transaction{tx}); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetTransaction("txq1")
	if err != nil || got == nil || got.ToAddr != bob {
		t.Fatalf("查交易失敗: %v %v", err, got)
	}
	byAddr, err := c.GetTransactionsByAddress(bob, 50)
	if err != nil || len(byAddr) != 1 {
		t.Fatalf("按地址查交易失敗: %v %d", err, len(byAddr))
	}
	byBlock, err := c.GetTransactionsByBlock(1)
	if err != nil || len(byBlock) != 1 {
		t.Fatalf("按塊查交易失敗: %v %d", err, len(byBlock))
	}
}

func TestMempool(t *testing.T) {
	c := openTmpDB(t)
	if err := c.AddMempoolTx("a", map[string]any{"tx_hash": "a", "fee": "1"}, "sigA", "1"); err != nil {
		t.Fatal(err)
	}
	if err := c.AddMempoolTx("b", map[string]any{"tx_hash": "b", "fee": "5"}, "sigB", "5"); err != nil {
		t.Fatal(err)
	}
	if got := c.MempoolSize(); got != 2 {
		t.Fatalf("mempool size=%d want 2", got)
	}
	mp, err := c.GetMempool(100)
	if err != nil || len(mp) != 2 {
		t.Fatalf("get mempool 失敗: %v %d", err, len(mp))
	}
	// 高費用應排在前。
	if mp[0]["tx_hash"] != "b" {
		t.Errorf("mempool 應按費用降序, first=%v", mp[0]["tx_hash"])
	}
	if err := c.RemoveMempoolTx("a"); err != nil {
		t.Fatal(err)
	}
	if got := c.MempoolSize(); got != 1 {
		t.Errorf("移除後 size=%d want 1", got)
	}
}

func TestReorgRebuild(t *testing.T) {
	c := openTmpDB(t)
	alice, bob, proposer := newAddr(t), newAddr(t), newAddr(t)
	now := genesisWithAlloc(t, c, alice)

	zeroHash := strings.Repeat("0", 64)
	coinbase := BuildCoinbaseTx(1, proposer, now)
	tx := Transaction{
		TxHash: "txr1", FromAddr: alice, ToAddr: bob,
		Amount: "100", Fee: "2", Nonce: 0, Ts: now,
	}
	blk := &Block{
		Height: 1, Hash: "br1", PrevHash: &zeroHash, MerkleRoot: "mr",
		Proposer: "proposer", ProposerAddress: proposer,
		Ts: now, TxCount: 2, Difficulty: 1, Nonce: 7,
	}
	if err := c.InsertBlock(blk, []Transaction{coinbase, tx}); err != nil {
		t.Fatal(err)
	}

	// 分叉切換：刪除 height1 及之後。
	if err := c.TruncateFromHeight(1); err != nil {
		t.Fatal(err)
	}
	if got := c.GetTipHeight(); got != 0 {
		t.Fatalf("truncate 後 tip=%d want 0", got)
	}

	// 重新插入 height1。
	if err := c.InsertBlock(blk, []Transaction{coinbase, tx}); err != nil {
		t.Fatal(err)
	}

	// 從創世重建賬戶，狀態應與直接插入一致。
	tip, err := c.RebuildAccounts()
	if err != nil {
		t.Fatalf("rebuild 失敗: %v", err)
	}
	if tip != 1 {
		t.Errorf("rebuild tip=%d want 1", tip)
	}
	assertBalance(t, c, alice, 898)
	assertBalance(t, c, bob, 100)
	assertBalance(t, c, proposer, 12)

	cd, err := c.CumulativeDifficulty(0, 1)
	if err != nil || cd != 1 {
		t.Errorf("cumulative difficulty=%d want 1", cd)
	}
}
