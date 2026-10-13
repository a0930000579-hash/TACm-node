package chaindb

import (
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// TestReadCacheConsistency 驗證：讀命中快取、寫入後失效、值與無快取一致。
func TestReadCacheConsistency(t *testing.T) {
	c := openTmpDB(t)
	if c.cache == nil {
		t.Fatal("cache 未初始化")
	}
	alice, bob, proposer := newAddr(t), newAddr(t), newAddr(t)
	now := genesisWithAlloc(t, c, alice)

	// 讀未入賬地址（miss → "0" 並寫入快取）。
	if got := c.GetBalance(bob); got != "0" {
		t.Fatalf("bob 初始餘額=%s", got)
	}
	// 第二次讀應命中快取。
	if got := c.GetBalance(bob); got != "0" {
		t.Fatalf("bob 快取餘額=%s", got)
	}

	// height1：coinbase 10 + alice→bob 100（fee 2）。
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

	// 寫入後快取已失效：讀回最新 SQL 值。
	assertBalance(t, c, alice, 898)
	assertBalance(t, c, bob, 100)
	assertBalance(t, c, proposer, 12)
	if got := c.GetNonce(alice); got != 1 {
		t.Fatalf("alice nonce=%d", got)
	}
	// 交易快取：GetTransaction 兩次回同值。
	got1, err := c.GetTransaction("tx1")
	if err != nil || got1 == nil {
		t.Fatalf("GetTransaction tx1 err=%v", err)
	}
	got2, _ := c.GetTransaction("tx1")
	if got2 == nil || got2.Amount != got1.Amount || got2.FromAddr != got1.FromAddr {
		t.Fatal("交易快取回值不一致")
	}
	// 外部修改回傳副本不影響快取。
	got2.Amount = "99999"
	got3, _ := c.GetTransaction("tx1")
	if got3.Amount != "100" {
		t.Fatalf("交易快取被外部修改污染: %s", got3.Amount)
	}
}

// TestReadCacheInvalidateOnNextBlock 驗證連續出塊後快取失效正確。
func TestReadCacheInvalidateOnNextBlock(t *testing.T) {
	c := openTmpDB(t)
	alice, bob := newAddr(t), newAddr(t)
	now := genesisWithAlloc(t, c, alice)

	zeroHash := strings.Repeat("0", 64)
	// height1：alice→bob 10。
	tx1 := Transaction{TxHash: "tx1", FromAddr: alice, ToAddr: bob,
		Amount: "10", Fee: "0", Nonce: 0, Ts: now}
	if err := c.InsertBlock(&Block{Height: 1, Hash: "b1", PrevHash: &zeroHash,
		Proposer: "p", Ts: now, TxCount: 1}, []Transaction{tx1}); err != nil {
		t.Fatal(err)
	}
	if got, _ := strconv.ParseFloat(c.GetBalance(alice), 64); math.Abs(got-990) > 1e-9 {
		t.Fatalf("alice=%g", got)
	}
	// height2：alice→bob 再 10（快取應已失效於 height1 寫入；此讀先命中 height1 值）。
	tx2 := Transaction{TxHash: "tx2", FromAddr: alice, ToAddr: bob,
		Amount: "10", Fee: "0", Nonce: 1, Ts: now}
	if err := c.InsertBlock(&Block{Height: 2, Hash: "b2", PrevHash: &zeroHash,
		Proposer: "p", Ts: now, TxCount: 1}, []Transaction{tx2}); err != nil {
		t.Fatal(err)
	}
	if got, _ := strconv.ParseFloat(c.GetBalance(alice), 64); math.Abs(got-980) > 1e-9 {
		t.Fatalf("height2 後 alice=%g 期望 980", got)
	}
}

// TestReadCacheConcurrent 驗證並發讀寫無 race（配合 go test -race）。
func TestReadCacheConcurrent(t *testing.T) {
	c := openTmpDB(t)
	alice, bob := newAddr(t), newAddr(t)
	now := genesisWithAlloc(t, c, alice)
	// 預先入一筆交易供並發讀取。
	zeroHash := strings.Repeat("0", 64)
	tx := Transaction{TxHash: "ctx", FromAddr: alice, ToAddr: bob,
		Amount: "5", Fee: "0", Nonce: 0, Ts: now}
	if err := c.InsertBlock(&Block{Height: 1, Hash: "b1", PrevHash: &zeroHash,
		Proposer: "p", Ts: now, TxCount: 1}, []Transaction{tx}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = c.GetBalance(alice)
				_ = c.GetBalance(bob)
				_ = c.GetNonce(alice)
				if tx, err := c.GetTransaction("ctx"); err != nil || tx == nil {
					t.Errorf("並發讀交易失敗: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	// 讀值與預期一致。
	assertBalance(t, c, alice, 995)
	assertBalance(t, c, bob, 5)
}
