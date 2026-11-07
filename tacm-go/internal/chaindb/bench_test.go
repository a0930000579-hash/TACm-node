package chaindb

import (
	"fmt"
	"path/filepath"
	"testing"
)

// newBenchDB 建立基準測試用資料庫（記憶體快取目錄）。
func newBenchDB(b *testing.B) *ChainDB {
	b.Helper()
	db, err := Open(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })
	return db
}

// benchTx 產生一條測試交易。
func benchTx(i int) Transaction {
	return Transaction{
		TxHash:   fmt.Sprintf("txhash-%d-%d", i, i),
		FromAddr: "tx01PAK8RojPnZ5mRNJ5hUBSBqe9V37qgLGed",
		ToAddr:   "tx01PAK8RojPnZ5mRNJ5hUBSBqe9V37qgLGed",
		Amount:   "1.0",
		Fee:      "0.02",
		Nonce:    int64(i),
		Ts:       1700000000 + int64(i),
		Memo:     "bench",
	}
}

// BenchmarkInsertBlock 區塊打包吞吐：單區塊含 100 筆交易。
func BenchmarkInsertBlock(b *testing.B) {
	db := newBenchDB(b)
	txs := make([]Transaction, 100)
	for i := range txs {
		txs[i] = benchTx(i)
	}
	h := "0"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		prev := h
		blk := &Block{
			Height:          int64(i + 1),
			Hash:            fmt.Sprintf("blk-%d-%d", i, i),
			PrevHash:        &prev,
			MerkleRoot:      "root",
			Proposer:        "node1",
			ProposerAddress: "tx01PAK8RojPnZ5mRNJ5hUBSBqe9V37qgLGed",
			Ts:              1700000000 + int64(i),
			Difficulty:      1,
			Nonce:           int64(i),
		}
		if err := db.InsertBlock(blk, txs); err != nil {
			b.Fatal(err)
		}
		h = blk.Hash
	}
}

// BenchmarkGetBlocks 區塊讀取吞吐：連續讀取 200 個區塊。
func BenchmarkGetBlocks(b *testing.B) {
	db := newBenchDB(b)
	// 預先寫入 200 個區塊。
	prev := "0"
	for i := 1; i <= 200; i++ {
		blk := &Block{
			Height:          int64(i),
			Hash:            fmt.Sprintf("blk-%d", i),
			PrevHash:        &prev,
			MerkleRoot:      "root",
			Proposer:        "node1",
			ProposerAddress: "tx01PAK8RojPnZ5mRNJ5hUBSBqe9V37qgLGed",
			Ts:              1700000000 + int64(i),
			Difficulty:      1,
		}
		if err := db.InsertBlock(blk, nil); err != nil {
			b.Fatal(err)
		}
		prev = blk.Hash
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := db.GetBlocks(200, 0); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkMempoolDrain 記憶池吞吐：連續寫入並讀取 1000 筆交易。
func BenchmarkMempoolDrain(b *testing.B) {
	db := newBenchDB(b)
	payload := map[string]any{
		"from":   "tx01PAK8RojPnZ5mRNJ5hUBSBqe9V37qgLGed",
		"to":     "tx01PAK8RojPnZ5mRNJ5hUBSBqe9V37qgLGed",
		"amount": "1", "fee": "0.02", "nonce": 1, "ts": 1700000000,
		"signature": "sig", "pubkey": "pub",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < 1000; j++ {
			h := fmt.Sprintf("mempool-%d-%d", i, j)
			if err := db.AddMempoolTx(h, payload, "sig", "0.02"); err != nil {
				b.Fatal(err)
			}
		}
		if _, err := db.GetMempool(1000); err != nil {
			b.Fatal(err)
		}
	}
}
