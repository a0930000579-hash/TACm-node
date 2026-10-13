package chaindb

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
)

// putTx 直接寫入 mempool（繞過簽名驗證，專測佇列排序）。
func putTx(t *testing.T, db *ChainDB, txHash, from string, nonce int64, fee string) {
	t.Helper()
	payload := map[string]any{
		"from":  from,
		"to":    "tx0000000000000000000000000000000000000000000000000000000000000001",
		"amount": "1",
		"fee":   fee,
		"nonce": nonce,
		"ts":    int64(1700000000),
	}
	b, _ := json.Marshal(payload)
	if err := db.AddMempoolTx(txHash, payload, "sig", fee); err != nil {
		t.Fatalf("putTx %s: %v", txHash, err)
	}
	_ = b
}

// TestGetMempoolPendingNonceOrder：同一地址亂序 nonce → 由鏈上 nonce 起依序輸出。
func TestGetMempoolPendingNonceOrder(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "chain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	from := "tx0alice0000000000000000000000000000000000000000000000000000000001"
	putTx(t, db, "h2", from, 2, "2.0")
	putTx(t, db, "h0", from, 0, "0.5")
	putTx(t, db, "h1", from, 1, "1.0")

	got, err := db.GetMempoolPending(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 txs, got %d", len(got))
	}
	wantOrder := []string{"h0", "h1", "h2"}
	for i, w := range wantOrder {
		if got[i]["tx_hash"] != w {
			t.Fatalf("order[%d] = %v, want %s", i, got[i]["tx_hash"], w)
		}
	}
}

// TestGetMempoolPendingGap：nonce 1 缺失 → 0 可打包，2 跳過。
func TestGetMempoolPendingGap(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "chain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	from := "tx0bob000000000000000000000000000000000000000000000000000000000001"
	putTx(t, db, "h0", from, 0, "1.0")
	putTx(t, db, "h2", from, 2, "9.0") // gap：nonce1 缺失

	got, err := db.GetMempoolPending(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0]["tx_hash"] != "h0" {
		t.Fatalf("gap: want only h0, got %+v", got)
	}
}

// TestGetMempoolPendingSameNonceFee：同 nonce 兩筆 → 高 fee 勝出。
func TestGetMempoolPendingSameNonceFee(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "chain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	from := "tx0carol000000000000000000000000000000000000000000000000000000001"
	putTx(t, db, "low", from, 0, "0.1")
	putTx(t, db, "high", from, 0, "5.0")

	got, err := db.GetMempoolPending(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0]["tx_hash"] != "high" {
		t.Fatalf("same-nonce: want high-fee winner, got %+v", got)
	}
}

// TestGetMempoolPendingMultiAddrRoundRobin：兩地址各一筆，fee 高者先出。
func TestGetMempoolPendingMultiAddrRoundRobin(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "chain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	a := "tx0addrA0000000000000000000000000000000000000000000000000000000001"
	b := "tx0addrB0000000000000000000000000000000000000000000000000000000001"
	putTx(t, db, "ha", a, 0, "3.0")
	putTx(t, db, "hb", b, 0, "7.0")

	got, err := db.GetMempoolPending(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0]["tx_hash"] != "hb" || got[1]["tx_hash"] != "ha" {
		t.Fatalf("round-robin: want hb first, got %+v", got)
	}
}

// TestGetMempoolPendingLimit：limit 截斷。
func TestGetMempoolPendingLimit(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "chain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	from := "tx0dave0000000000000000000000000000000000000000000000000000000001"
	for i := int64(0); i < 5; i++ {
		putTx(t, db, fmt.Sprintf("h%d", i), from, i, "1.0")
	}
	got, err := db.GetMempoolPending(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("limit: want 2, got %d", len(got))
	}
}
