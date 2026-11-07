package wallet

import (
	"math/big"
	"testing"
	"time"

	"tacm/internal/chaindb"
)

// TestTreasuryFeeSplit M151c-v2：treasury_fee_bps 開啟後，鏈上交易手續費按比例
// 拆分入治理金庫（treasury_pool），其餘維持入 fee 帳戶（交易所做市資金）。
func TestTreasuryFeeSplit(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := NewService(st)
	svc.SetTreasuryFeeBps(5000) // 50% 手續費入治理金庫

	if err := svc.Deposit("A", AssetTACm, amt("100", AssetTACm), "deposit"); err != nil {
		t.Fatal(err)
	}
	before, _ := svc.Balance(TreasuryAddr)
	beforeF := new(big.Int).Set(before.TACmBalance)

	tx := chaindb.Transaction{
		TxHash: "tx-split-1", FromAddr: "A", ToAddr: "B",
		Amount: "10", Fee: "0.021", Ts: time.Now().Unix(),
	}
	if err := svc.ApplyBlock(1, []chaindb.Transaction{tx}); err != nil {
		t.Fatalf("ApplyBlock: %v", err)
	}

	after, _ := svc.Balance(TreasuryAddr)
	delta := new(big.Int).Sub(after.TACmBalance, beforeF)
	// fee=0.021 TACm=21e15 raw；50% = 10.5e15。
	want := big.NewInt(10500000000000000)
	lo := new(big.Int).Sub(want, big.NewInt(1000))
	hi := new(big.Int).Add(want, big.NewInt(1000))
	if delta.Cmp(lo) < 0 || delta.Cmp(hi) > 0 {
		t.Fatalf("治理金庫應收到約一半手續費（%v），得 %v", want, delta)
	}

	// fee 帳戶仍收另一半（0.0105 TACm=10.5e15）。
	feeAcct, _ := svc.Balance("fee")
	feeWant := big.NewInt(10500000000000000)
	if feeAcct.TACmBalance.Cmp(lo) < 0 || feeAcct.TACmBalance.Cmp(hi) > 0 {
		t.Fatalf("fee 帳戶應收另一半（%v），得 %v", feeWant, feeAcct.TACmBalance)
	}
	// 帳本守恆：A 扣 10+0.021。
	a, _ := svc.Balance("A")
	wantA := big.NewInt(0).Set(amt("100", AssetTACm).Big)
	wantA.Sub(wantA, amt("10.021", AssetTACm).Big)
	if a.TACmBalance.Cmp(wantA) != 0 {
		t.Fatalf("A 應餘 %v，得 %v", wantA, a.TACmBalance)
	}
	b, _ := svc.Balance("B")
	if b.TACmBalance.Cmp(amt("10", AssetTACm).Big) != 0 {
		t.Fatalf("B 應收 10，得 %v", b.TACmBalance)
	}
}

// TestTreasuryFeeSplitDisabled：treasury_fee_bps=0（關閉）時維持原行為。
func TestTreasuryFeeSplitDisabled(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := NewService(st) // 預設 0

	if err := svc.Deposit("A", AssetTACm, amt("100", AssetTACm), "deposit"); err != nil {
		t.Fatal(err)
	}
	tx := chaindb.Transaction{
		TxHash: "tx-split-0", FromAddr: "A", ToAddr: "B",
		Amount: "10", Fee: "0.021", Ts: time.Now().Unix(),
	}
	if err := svc.ApplyBlock(1, []chaindb.Transaction{tx}); err != nil {
		t.Fatalf("ApplyBlock: %v", err)
	}
	tr, _ := svc.Balance(TreasuryAddr)
	if tr.TACmBalance.Sign() != 0 {
		t.Fatalf("關閉時治理金庫應為 0，得 %v", tr.TACmBalance)
	}
	feeAcct, _ := svc.Balance("fee")
	if feeAcct.TACmBalance.Cmp(amt("0.021", AssetTACm).Big) != 0 {
		t.Fatalf("關閉時 fee 帳戶應收全額 0.021，得 %v", feeAcct.TACmBalance)
	}
}
