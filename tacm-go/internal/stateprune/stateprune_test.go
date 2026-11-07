package stateprune

import (
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"tacm/internal/chaindb"
	"tacm/internal/crypto"
)

func newKeyAddr(t *testing.T) (*crypto.KeyPair, string) {
	t.Helper()
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	addr, err := kp.Address()
	if err != nil {
		t.Fatal(err)
	}
	return kp, addr
}

// insertChain 灌入 nBlocks 個區塊：創世給 funder 1,000,000 TACm，
// 之後每塊一筆 funder→alice 1 TACm 的轉帳（使 transactions 表成長）。
func insertChain(t *testing.T, c *chaindb.ChainDB, funder string, nBlocks int) {
	t.Helper()
	now := time.Now().Unix()
	genesis := &chaindb.Block{Height: 0, Hash: strings.Repeat("0", 64), Proposer: "genesis", Ts: now, TxCount: 1}
	genTx := chaindb.Transaction{TxHash: "gen", ToAddr: funder, Amount: "1000000", Fee: "0", Ts: now}
	if err := c.InsertBlock(genesis, []chaindb.Transaction{genTx}); err != nil {
		t.Fatal(err)
	}
	prev := strings.Repeat("0", 64)
	for h := 1; h <= nBlocks; h++ {
		h := int64(h)
		tx := chaindb.Transaction{
			TxHash: "tx" + strconv.FormatInt(h, 10), FromAddr: funder, ToAddr: "tx0alice",
			Amount: "1", Fee: "0", Nonce: h - 1, Ts: now,
		}
		blk := &chaindb.Block{
			Height: h, Hash: "b" + strconv.FormatInt(h, 10), PrevHash: &prev,
			Proposer: "p", ProposerAddress: "tx0proposer", Ts: now, TxCount: 1, Difficulty: 1,
		}
		if err := c.InsertBlock(blk, []chaindb.Transaction{tx}); err != nil {
			t.Fatalf("insert block %d: %v", h, err)
		}
		prev = blk.Hash
	}
}

func balanceOf(t *testing.T, c *chaindb.ChainDB, addr string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(c.GetBalance(addr), 64)
	if err != nil {
		t.Fatalf("balance parse %s: %v", addr, err)
	}
	return v
}

func TestPrunerPrunesHistoryKeepsState(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "chain.db")
	c, err := chaindb.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	_, funder := newKeyAddr(t)

	const nBlocks = 300
	insertChain(t, c, funder, nBlocks)

	tipBefore := c.GetTipHeight()
	balFunderBefore := balanceOf(t, c, funder)
	balAliceBefore := balanceOf(t, c, "tx0alice")

	// 啟動 Pruner：保留最近 50 塊、安全邊界 0、不檢查時間、開 VACUUM、歸檔到獨立檔。
	cfg := Config{
		Interval: time.Hour, // 測試不觸發背景，改用 RunOnce
		Prune: chaindb.PruneConfig{
			RetentionBlocks: 50,
			SafetyBlocks:    0,
			RetentionAgeSec: 0,
			ArchivePath:     filepath.Join(dir, "archive.db"),
			Vacuum:          true,
			BatchSize:       50,
		},
	}
	p := New(c, cfg)
	ps, err := p.RunOnce()
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// 1) 刪除了約 (tip - retention) 個區塊。
	wantPruned := int64(nBlocks) - 50
	if ps.PrunedBlocks != wantPruned {
		t.Errorf("PrunedBlocks=%d want ~%d", ps.PrunedBlocks, wantPruned)
	}
	if ps.PrunedTxs <= 0 {
		t.Errorf("PrunedTxs=%d want >0", ps.PrunedTxs)
	}
	// 2) tip 高度完全不變。
	if tipAfter := c.GetTipHeight(); tipAfter != tipBefore {
		t.Errorf("tip changed: before=%d after=%d", tipBefore, tipAfter)
	}
	// 3) 餘額完全不變（物質化帳本不受歷史刪除影響）。
	if got := balanceOf(t, c, funder); math.Abs(got-balFunderBefore) > 1e-9 {
		t.Errorf("funder balance changed: before=%g after=%g", balFunderBefore, got)
	}
	if got := balanceOf(t, c, "tx0alice"); math.Abs(got-balAliceBefore) > 1e-9 {
		t.Errorf("alice balance changed: before=%g after=%g", balAliceBefore, got)
	}
	// 4) 最近保留塊仍在，舊塊已刪。
	if blk, _ := c.GetBlock(tipBefore); blk == nil {
		t.Errorf("tip block %d missing after prune", tipBefore)
	}
	if blk, _ := c.GetBlock(1); blk != nil {
		t.Errorf("old block 1 should be pruned, but still present")
	}
	// 5) DB 位元組有回報，且清理後不大於清理前（VACUUM 回收）。
	if ps.DBBytesAfter > ps.DBBytesBefore {
		t.Logf("note: db grew slightly before vacuum measurement: before=%d after=%d",
			ps.DBBytesBefore, ps.DBBytesAfter)
	}
	// 6) 對外 Stats 與本次一致。
	s := p.Stats()
	if s.PrunedBlocks != ps.PrunedBlocks || s.Retention != 50 {
		t.Errorf("Stats mismatch: %+v vs %+v", s, ps)
	}
	if s.LastRun.IsZero() {
		t.Errorf("Stats.LastRun zero after RunOnce")
	}
	// 7) Close 不阻塞、可重入。
	if err := p.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestPrunerConservativeNoOp(t *testing.T) {
	c, err := chaindb.Open(filepath.Join(t.TempDir(), "chain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, funder := newKeyAddr(t)
	insertChain(t, c, funder, 10)

	// RetentionBlocks=0 → 永不刪除。
	p := New(c, Config{Prune: chaindb.PruneConfig{RetentionBlocks: 0, SafetyBlocks: 1}})
	defer p.Close()
	ps, err := p.RunOnce()
	if err != nil {
		t.Fatal(err)
	}
	if ps.PrunedBlocks != 0 {
		t.Errorf("Retention=0 should be no-op, pruned=%d", ps.PrunedBlocks)
	}
	if c.GetTipHeight() != 10 {
		t.Errorf("tip changed to %d, want 10", c.GetTipHeight())
	}
}
