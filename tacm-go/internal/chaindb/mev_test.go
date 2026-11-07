package chaindb

import (
	"math"
	"path/filepath"
	"testing"
)

// putTx1559 直接寫入一筆 EIP-1559 type2 交易（繞過簽名）。
func putTx1559(t *testing.T, db *ChainDB, txHash, from string, nonce int64,
	fee, maxFee, prio string, gasLimit int64) {
	t.Helper()
	payload := map[string]any{
		"from":         from,
		"to":           "tx0000000000000000000000000000000000000000000000000000000000000001",
		"amount":       "1",
		"fee":          fee,
		"nonce":        nonce,
		"ts":           int64(1700000000),
		"max_fee":      maxFee,
		"priority_fee": prio,
		"gas_limit":    gasLimit,
	}
	if err := db.AddMempoolTx(txHash, payload, "sig", fee); err != nil {
		t.Fatalf("putTx1559 %s: %v", txHash, err)
	}
}

func almostEq(a, b float64) bool { return math.Abs(a-b) <= 1e-9 }

// TestEffectivePriorityTip1559 驗證 EIP-1559 有效小費公式 min(priority, max_fee-baseFee)×gas 之邊界。
func TestEffectivePriorityTip1559(t *testing.T) {
	const base = 0.0000002 // TACm/gas
	const gas = int64(21000)

	// 一般：priority 小於 headroom → tip = priority × gas。
	tx := map[string]any{"max_fee": "0.0001", "priority_fee": "0.0000005", "gas_limit": gas}
	want := 0.0000005 * float64(gas)
	if got := EffectivePriorityTip(tx, base); !almostEq(got, want) {
		t.Fatalf("一般 tip = %g, want %g", got, want)
	}

	// headroom < priority → tip 被 headroom 截斷（max_fee 不足以同時覆蓋 base+prio）。
	tx = map[string]any{"max_fee": "0.0000003", "priority_fee": "0.0000005", "gas_limit": gas}
	want = (0.0000003 - base) * float64(gas)
	if got := EffectivePriorityTip(tx, base); !almostEq(got, want) {
		t.Fatalf("headroom 截斷 tip = %g, want %g", got, want)
	}

	// max_fee == baseFee → headroom=0 → tip=0（不可打包）。
	tx = map[string]any{"max_fee": "0.0000002", "priority_fee": "0.0000005", "gas_limit": gas}
	if got := EffectivePriorityTip(tx, base); got != 0 {
		t.Fatalf("max_fee==baseFee 應 tip=0, got %g", got)
	}

	// max_fee < baseFee → tip=0（排最後）。
	tx = map[string]any{"max_fee": "0.0000001", "priority_fee": "0.0000005", "gas_limit": gas}
	if got := EffectivePriorityTip(tx, base); got != 0 {
		t.Fatalf("max_fee<baseFee 應 tip=0, got %g", got)
	}

	// priority=0 → tip=0。
	tx = map[string]any{"max_fee": "0.0001", "priority_fee": "0", "gas_limit": gas}
	if got := EffectivePriorityTip(tx, base); got != 0 {
		t.Fatalf("priority=0 應 tip=0, got %g", got)
	}

	// 欄位缺失/非法字串 → 視為 0，不 panic。
	tx = map[string]any{"max_fee": "abc", "priority_fee": "", "gas_limit": 0}
	if got := EffectivePriorityTip(tx, base); got != 0 {
		t.Fatalf("非法欄位應 tip=0, got %g", got)
	}
}

// TestEffectivePriorityTipLegacy 驗證 legacy 交易（無 1559 欄位）有效小費=總 fee。
func TestEffectivePriorityTipLegacy(t *testing.T) {
	tx := map[string]any{"fee": "3.14"}
	if got := EffectivePriorityTip(tx, 0.000001); !almostEq(got, 3.14) {
		t.Fatalf("legacy tip = %g, want 3.14", got)
	}
	// 無 fee 欄位 → 0。
	tx = map[string]any{}
	if got := EffectivePriorityTip(tx, 0.000001); got != 0 {
		t.Fatalf("legacy 無 fee 應 tip=0, got %g", got)
	}
}

// TestMempoolSortedTipOrder：高有效小費的地址隊首先出。
func TestMempoolSortedTipOrder(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "chain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// baseFee = 2e-7 TACm/gas。
	const base = 0.0000002
	// A：priority 高 → 有效 tip 高。
	a := "tx0tipA0000000000000000000000000000000000000000000000000000000001"
	// B：priority 低 → 有效 tip 低（但 max_fee 都夠）。
	b := "tx0tipB00000000000000000000000000000000000000000000000000000000000001"
	putTx1559(t, db, "low", b, 0, "0.0063", "0.0001", "0.0000001", 21000)
	putTx1559(t, db, "high", a, 0, "0.0126", "0.0001", "0.0000004", 21000)

	got, err := db.GetMempoolPendingSorted(10, base, PackPriorityTip)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0]["tx_hash"] != "high" || got[1]["tx_hash"] != "low" {
		t.Fatalf("tip 降序應 high→low, got %v", hashes(got))
	}
}

// TestMempoolSortedTieBreakDeterministic：有效小費相同時，依 tx_hash 字典序（received_at 同秒）。
func TestMempoolSortedTieBreakDeterministic(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "chain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	x := "tx0tieX0000000000000000000000000000000000000000000000000000000000001"
	y := "tx0tieY00000000000000000000000000000000000000000000000000000000000001"
	// 兩筆有效小費相同（同 priority/同 gas）；故意後插入 y、先插入 z-... 驗證非「先到者贏」。
	putTx(t, db, "hash_b", y, 0, "2.0")
	putTx(t, db, "hash_a", x, 0, "2.0")

	got, err := db.GetMempoolPendingSorted(10, 0, PackPriorityTip)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	// 小費相同 → 確定性 tie-break：tx_hash 字典序 hash_a < hash_b。
	if got[0]["tx_hash"] != "hash_a" || got[1]["tx_hash"] != "hash_b" {
		t.Fatalf("相同小費應依 hash 字典序, got %v", hashes(got))
	}
}

// TestMempoolSortedNonceContinuity 保持：nonce 連續、gap 截斷、同 nonce 高 fee 覆蓋。
func TestMempoolSortedNonceContinuity(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "chain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	from := "tx0cont000000000000000000000000000000000000000000000000000000000001"
	putTx(t, db, "n0low", from, 0, "0.1") // 先插低 fee
	putTx(t, db, "n0", from, 0, "1.0")    // 同 nonce 高 fee 覆蓋低 fee（RBF 替換）
	putTx(t, db, "n1", from, 1, "1.0")
	putTx(t, db, "n3", from, 3, "9.0") // gap：nonce2 缺失

	got, err := db.GetMempoolPendingSorted(10, 0, PackPriorityTip)
	if err != nil {
		t.Fatal(err)
	}
	// n0(高 fee 覆蓋) → n1；n3 因 gap 截斷。
	if len(got) != 2 || got[0]["tx_hash"] != "n0" || got[1]["tx_hash"] != "n1" {
		t.Fatalf("nonce 連續+gap 截斷應 n0→n1, got %v", hashes(got))
	}
}

// TestMempoolSortedRBF：低 fee 同 from+nonce 回 ErrMempoolUnderpriced；高 fee 才替換。
func TestMempoolSortedRBF(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "chain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	from := "tx0rbf00000000000000000000000000000000000000000000000000000000000001"
	if err := putTxErr(db, "orig", from, 0, "1.0"); err != nil {
		t.Fatalf("orig: %v", err)
	}
	// 低 fee 替換 → ErrMempoolUnderpriced。
	if err := putTxErr(db, "low", from, 0, "0.5"); err != ErrMempoolUnderpriced {
		t.Fatalf("低 fee 替換應 ErrMempoolUnderpriced, got %v", err)
	}
	// 高 fee 替換 → 成功。
	if err := putTxErr(db, "high", from, 0, "2.0"); err != nil {
		t.Fatalf("高 fee 替換應成功, got %v", err)
	}
	got, err := db.GetMempoolPendingSorted(10, 0, PackPriorityTip)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0]["tx_hash"] != "high" {
		t.Fatalf("RBF 後應只剩 high, got %v", hashes(got))
	}
}

// TestMempoolSortedStrategies：不同策略順序符合預期。
func TestMempoolSortedStrategies(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "chain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// A：小費低但 gas 大；B：小費高但 gas 小。
	a := "tx0strA000000000000000000000000000000000000000000000000000000000001"
	b := "tx0strB00000000000000000000000000000000000000000000000000000000000001"
	putTx1559(t, db, "biggas_lowtip", a, 0, "0.0021", "0.0001", "0.00000001", 210000)
	putTx1559(t, db, "smallgas_hightip", b, 0, "0.0084", "0.0001", "0.0000004", 21000)

	const base = 0.0000002
	// PriorityTip：高 tip 先（smallgas_hightip）。
	got, _ := db.GetMempoolPendingSorted(10, base, PackPriorityTip)
	if got[0]["tx_hash"] != "smallgas_hightip" {
		t.Fatalf("PriorityTip 應 hightip 先, got %v", hashes(got))
	}
	// GasUsage：高 gas 先（biggas_lowtip）。
	got, _ = db.GetMempoolPendingSorted(10, base, PackGasUsage)
	if got[0]["tx_hash"] != "biggas_lowtip" {
		t.Fatalf("GasUsage 應高 gas 先, got %v", hashes(got))
	}
	// FIFO：received_at 同秒 → tie-break hash 字典序（biggas_lowtip < smallgas_hightip）。
	got, _ = db.GetMempoolPendingSorted(10, base, PackFIFO)
	if got[0]["tx_hash"] != "biggas_lowtip" {
		t.Fatalf("FIFO 同秒應 hash 字典序, got %v", hashes(got))
	}
}

// TestGetMempoolPendingDelegates：舊方法仍可運作（legacy 行為不變）。
func TestGetMempoolPendingDelegates(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "chain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := "tx0oldA0000000000000000000000000000000000000000000000000000000000001"
	b := "tx0oldB00000000000000000000000000000000000000000000000000000000000001"
	putTx(t, db, "low", a, 0, "1.0")
	putTx(t, db, "high", b, 0, "5.0")
	got, err := db.GetMempoolPending(10)
	if err != nil {
		t.Fatal(err)
	}
	if got[0]["tx_hash"] != "high" {
		t.Fatalf("舊方法應高 fee 先, got %v", hashes(got))
	}
}

func hashes(got []map[string]any) []string {
	out := make([]string, 0, len(got))
	for _, g := range got {
		out = append(out, getString(g, "tx_hash"))
	}
	return out
}

// putTxErr 回傳錯誤版本（供 RBF 斷言）。
func putTxErr(db *ChainDB, txHash, from string, nonce int64, fee string) error {
	payload := map[string]any{
		"from":   from,
		"to":     "tx0000000000000000000000000000000000000000000000000000000000000001",
		"amount": "1",
		"fee":    fee,
		"nonce":  nonce,
		"ts":     int64(1700000000),
	}
	return db.AddMempoolTx(txHash, payload, "sig", fee)
}
