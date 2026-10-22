package chaindb

import (
	"errors"
	"path/filepath"
	"testing"
)


// TestMempoolRBFReplace 同 sender+nonce 高 fee 取代低 fee（Ethereum RBF）。
func TestMempoolRBFReplace(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "tac.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	from := "tx01AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	low := map[string]any{"from": from, "nonce": int64(3), "to": "tx01BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", "value": "1"}
	high := map[string]any{"from": from, "nonce": int64(3), "to": "tx01BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", "value": "2"}

	if err := db.AddMempoolTx("hash-low", low, "sig", "0.5"); err != nil {
		t.Fatalf("插入低 fee 失敗: %v", err)
	}
	// 高 fee 替換成功
	if err := db.AddMempoolTx("hash-high", high, "sig", "1.5"); err != nil {
		t.Fatalf("高 fee 替換失敗: %v", err)
	}
	pool, err := db.GetMempool(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pool) != 1 {
		t.Fatalf("替換後應剩 1 筆, got %d", len(pool))
	}
	if pool[0]["tx_hash"] != "hash-high" {
		t.Fatalf("應保留高 fee 交易, got %v", pool[0]["tx_hash"])
	}
}

// TestMempoolRBFUnderpriced 同 sender+nonce 低 fee 提交應被拒絕。
func TestMempoolRBFUnderpriced(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "tac.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	from := "tx01CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
	tx := map[string]any{"from": from, "nonce": int64(7), "to": "tx01DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD", "value": "1"}
	if err := db.AddMempoolTx("hash-a", tx, "sig", "2.0"); err != nil {
		t.Fatalf("首次插入失敗: %v", err)
	}
	// 更低 fee 應被拒
	err = db.AddMempoolTx("hash-b", tx, "sig", "1.0")
	if !errors.Is(err, ErrMempoolUnderpriced) {
		t.Fatalf("低 fee 應回 ErrMempoolUnderpriced, got %v", err)
	}
	// 池內仍是原交易
	pool, _ := db.GetMempool(10)
	if len(pool) != 1 || pool[0]["tx_hash"] != "hash-a" {
		t.Fatalf("池內應保留原交易, got %v", pool)
	}
	// 相等 fee 也應被拒
	err = db.AddMempoolTx("hash-c", tx, "sig", "2.0")
	if !errors.Is(err, ErrMempoolUnderpriced) {
		t.Fatalf("相等 fee 應被拒, got %v", err)
	}
}

// TestMempoolRBFBatch 批量落池同樣執行 RBF。
func TestMempoolRBFBatch(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "tac.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	from := "tx01EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE"
	tx1 := map[string]any{"from": from, "nonce": int64(1), "to": "tx01FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF", "value": "1"}
	tx2 := map[string]any{"from": from, "nonce": int64(1), "to": "tx01FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF", "value": "2"}
	entries := []MempoolEntry{
		{TxHash: "hash-x", Payload: tx1, Signature: "sig", Fee: "0.3"},
		{TxHash: "hash-y", Payload: tx2, Signature: "sig", Fee: "0.9"},
	}
	if err := db.AddMempoolTxs(entries); err != nil {
		t.Fatalf("批量插入失敗: %v", err)
	}
	pool, _ := db.GetMempool(10)
	if len(pool) != 1 || pool[0]["tx_hash"] != "hash-y" {
		t.Fatalf("批量 RBF 應保留高 fee, got %v", pool)
	}
}
