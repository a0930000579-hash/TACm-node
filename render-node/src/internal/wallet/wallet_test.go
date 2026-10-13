package wallet

import (
	"math/big"
	"sync"
	"testing"

	"tacm/internal/chaindb"
)

func bigS(s string) *big.Int {
	v, _ := new(big.Int).SetString(s, 10)
	return v
}

func amt(s string, a Asset) Amount {
	v, err := NewAmount(s, a)
	if err != nil {
		panic(err)
	}
	return v
}

// TestAmountParseFormat：金額字符串 ↔ 最小單位互轉（含大於 int64 的 TACm）。
func TestAmountParseFormat(t *testing.T) {
	cases := []struct {
		in   string
		as   Asset
		want string // 最小單位十進制
	}{
		{"1", AssetTACm, "1000000000000000000"},
		{"0.5", AssetTACm, "500000000000000000"},
		{"1000", AssetTACm, "1000000000000000000000"}, // 超出 int64 → big.Int
		{"0.000001", AssetTiUSD, "1"},
		{"1.25", AssetUSDT, "1250000"},
		{"1234.567890", AssetTiUSD, "1234567890"},
		{"0", AssetTACm, "0"},
	}
	for _, c := range cases {
		got, err := NewAmount(c.in, c.as)
		if err != nil {
			t.Fatalf("NewAmount(%q,%s) 錯誤: %v", c.in, c.as, err)
		}
		// String 往返允許去尾零（1234.567890 → 1234.56789）。
		if _, err := NewAmount(got.String(c.as), c.as); err != nil {
			t.Errorf("String 往返失敗: %q → %q (%v)", c.in, got.String(c.as), err)
		}
		wantBig, _ := new(big.Int).SetString(c.want, 10)
		if c.as == AssetTACm {
			if got.Big.Cmp(wantBig) != 0 {
				t.Errorf("NewAmount(%q)=%s want %s", c.in, got.Big, c.want)
			}
		} else if got.I64 != wantBig.Int64() {
			t.Errorf("NewAmount(%q)=%d want %s", c.in, got.I64, c.want)
		}
	}
}

// 非法金額拒絕。
func TestAmountParseRejects(t *testing.T) {
	bad := []string{"", "abc", "1.2.3", "1.0000001", "-5", "1e3", "１２３"}
	for _, s := range bad {
		if _, err := NewAmount(s, AssetTiUSD); err == nil {
			t.Errorf("NewAmount(%q) 應報錯", s)
		}
	}
	// 穩定幣超 int64 拒絕。
	if _, err := NewAmount("10000000000000", AssetTiUSD); err == nil {
		t.Error("穩定幣超 int64 應報錯")
	}
}

// TestFeeOf：定點手續費（向上取整；big 防溢出）。
func TestFeeOf(t *testing.T) {
	// TiUSD 0.5%：1_000_000 micro → 5_000。
	if got := FeeOf(big.NewInt(1_000_000), AssetTiUSD.FeeBps()); got.Int64() != 5_000 {
		t.Errorf("TiUSD fee=%d want 5000", got)
	}
	// USDT 1.25%：1_000_000 → 12_500。
	if got := FeeOf(big.NewInt(1_000_000), AssetUSDT.FeeBps()); got.Int64() != 12_500 {
		t.Errorf("USDT fee=%d want 12500", got)
	}
	// TACm 2%：1e18 → 2e16。
	if got := FeeOf(big.NewInt(1_000_000_000_000_000_000), AssetTACm.FeeBps()); got.Int64() != 20_000_000_000_000_000 {
		t.Errorf("TACm fee=%d", got)
	}
	// 向上取整：1 micro × 50bp = 0.005 → ceil → 1。
	if got := FeeOf(big.NewInt(1), AssetTiUSD.FeeBps()); got.Int64() != 1 {
		t.Errorf("ceil fee=%d want 1", got)
	}
	// 大額 TACm（1000 TACM = 1e21 wei）2% = 2e19，不溢出。
	bigAmt := new(big.Int).Mul(big.NewInt(1000), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	want := new(big.Int).Mul(big.NewInt(20), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	if got := FeeOf(bigAmt, AssetTACm.FeeBps()); got.Cmp(want) != 0 {
		t.Errorf("大額 TACm fee=%s want %s", got, want)
	}
}

// TestTransfer：TiUSD 轉帳（0.5% 手續費）雙分錄 + 餘額更新 + 審計。
func TestTransfer(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := NewService(st)

	if err := svc.Deposit("A", AssetTiUSD, amt("100", AssetTiUSD), "deposit"); err != nil {
		t.Fatal(err)
	}
	fee, err := svc.Transfer("A", "B", AssetTiUSD, amt("10", AssetTiUSD), "pay")
	if err != nil {
		t.Fatal(err)
	}
	if fee.I64 != 50_000 { // 10,000,000 × 0.5% = 50,000
		t.Fatalf("fee=%d want 50000", fee.I64)
	}
	a, _ := svc.Balance("A")
	if a.TiUSDBalance != 100_000_000-10_000_000-50_000 {
		t.Fatalf("A 餘額=%d", a.TiUSDBalance)
	}
	b, _ := svc.Balance("B")
	if b.TiUSDBalance != 10_000_000 {
		t.Fatalf("B 餘額=%d", b.TiUSDBalance)
	}
	led, _ := svc.Ledger(10)
	if len(led) != 4 { // deposit + 3
		t.Fatalf("分錄數=%d want 4", len(led))
	}
}

// TestTransferUSDTFee：USDT 1.25% 手續費。
func TestTransferUSDTFee(t *testing.T) {
	st, _ := Open(t.TempDir())
	defer st.Close()
	svc := NewService(st)
	if err := svc.Deposit("A", AssetUSDT, amt("100", AssetUSDT), "d"); err != nil {
		t.Fatal(err)
	}
	fee, err := svc.Transfer("A", "B", AssetUSDT, amt("80", AssetUSDT), "p")
	if err != nil {
		t.Fatal(err)
	}
	if fee.I64 != 1_000_000 { // 80M × 1.25% = 1M
		t.Fatalf("fee=%d want 1000000", fee.I64)
	}
	a, _ := svc.Balance("A")
	if a.USDTBalance != 100_000_000-80_000_000-1_000_000 {
		t.Fatalf("A USDT=%d", a.USDTBalance)
	}
}

// TestTransferTACmBig：大額 TACm 轉帳（1000 TACM > int64 wei 上限）正常入帳。
func TestTransferTACmBig(t *testing.T) {
	st, _ := Open(t.TempDir())
	defer st.Close()
	svc := NewService(st)
	bigAmt := amt("1000", AssetTACm)
	if err := svc.Deposit("A", AssetTACm, bigAmt, "d"); err != nil {
		t.Fatal(err)
	}
	fee, err := svc.Transfer("A", "B", AssetTACm, amt("500", AssetTACm), "big")
	if err != nil {
		t.Fatal(err)
	}
	// fee = 500×2% = 10 TACM。
	if fee.Big.Cmp(bigS("10000000000000000000")) != 0 {
		t.Fatalf("fee=%s want 10 TACM", fee.Big)
	}
	a, _ := svc.Balance("A")
	// 1000 - 500 - 10 = 490。
	if a.TACmBalance.Cmp(bigS("490000000000000000000")) != 0 {
		t.Fatalf("A 餘額=%s want 490 TACM", a.TACmBalance)
	}
	b, _ := svc.Balance("B")
	if b.TACmBalance.Cmp(bigS("500000000000000000000")) != 0 {
		t.Fatalf("B 餘額=%s want 500 TACM", b.TACmBalance)
	}
}

// TestInsufficientBalance：餘額不足必須報錯且帳本不變。
func TestInsufficientBalance(t *testing.T) {
	st, _ := Open(t.TempDir())
	defer st.Close()
	svc := NewService(st)
	if err := svc.Deposit("A", AssetTACm, amt("0.0001", AssetTACm), "d"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Transfer("A", "B", AssetTACm, amt("1", AssetTACm), "big"); err == nil {
		t.Fatal("餘額不足應報錯")
	}
	b, _ := svc.Balance("B")
	if b.TACmBalance.Sign() != 0 {
		t.Fatalf("失敗轉帳不得入帳: %s", b.TACmBalance)
	}
	a, _ := svc.Balance("A")
	if a.TACmBalance.Cmp(bigS("100000000000000")) != 0 {
		t.Fatalf("失敗轉帳扣款: %s", a.TACmBalance)
	}
}

// TestTiUSDMintBurn：發行/銷毀與供給摘要、帳戶入帳。
func TestTiUSDMintBurn(t *testing.T) {
	st, _ := Open(t.TempDir())
	defer st.Close()
	svc := NewService(st)

	sup, err := svc.MintTiUSD("Vault", 5_000_000, "mint to vault")
	if err != nil {
		t.Fatal(err)
	}
	if sup != 5_000_000 {
		t.Fatalf("供給=%d want 5000000", sup)
	}
	v, _ := svc.Balance("Vault")
	if v.TiUSDBalance != 5_000_000 {
		t.Fatalf("Vault TiUSD=%d", v.TiUSDBalance)
	}
	sup, err = svc.BurnTiUSD(2_000_000, "burn")
	if err != nil {
		t.Fatal(err)
	}
	if sup != 3_000_000 {
		t.Fatalf("銷毀後供給=%d want 3000000", sup)
	}
	sum, _ := svc.TiUSDSummary()
	if sum.TotalMinted != 5_000_000 || sum.TotalBurned != 2_000_000 {
		t.Fatalf("供給摘要異常: mint=%d burn=%d", sum.TotalMinted, sum.TotalBurned)
	}
	if _, err := svc.BurnTiUSD(10_000_000, "over"); err == nil {
		t.Fatal("超額銷毀應報錯")
	}
}

// TestConcurrentTransfers：並發轉帳 -race 下帳本守恆。
func TestConcurrentTransfers(t *testing.T) {
	st, _ := Open(t.TempDir())
	defer st.Close()
	svc := NewService(st)
	if err := svc.Deposit("Hub", AssetTiUSD, amt("10000", AssetTiUSD), "seed"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			to := "U" + string(rune('A'+i%5))
			_, _ = svc.Transfer("Hub", to, AssetTiUSD, amt("0.1", AssetTiUSD), "p")
		}(i)
	}
	wg.Wait()
	hub, _ := svc.Balance("Hub")
	// 10000 - 20×(0.1 + fee 0.0005) = 10000 - 2.01。
	want := amt("9997.99", AssetTiUSD)
	if hub.TiUSDBalance != want.I64 {
		t.Fatalf("Hub 餘額=%d want %d（並發不應丟失資金）", hub.TiUSDBalance, want.I64)
	}
	// 全體餘額 + fee = 初始（守恆）。
	led, _ := svc.Ledger(500)
	var sum int64
	for _, e := range led {
		if e.Kind != KindDeposit {
			v, _ := new(big.Int).SetString(e.Delta, 10)
			sum += v.Int64()
		}
	}
	if sum != 0 {
		t.Fatalf("分錄總和≠0: %d", sum)
	}
}

// TestApplyBlock：鏈上區塊同步（coinbase + 轉帳 + 手續費）。
func TestApplyBlock(t *testing.T) {
	st, _ := Open(t.TempDir())
	defer st.Close()
	svc := NewService(st)

	tx1 := []chaindb.Transaction{{
		TxHash: "cb1", FromAddr: "", ToAddr: "miner1", Amount: "10", Fee: "0", Memo: "coinbase",
	}}
	if err := svc.ApplyBlock(1, tx1); err != nil {
		t.Fatal(err)
	}
	// 重複同步同高度：冪等。
	if err := svc.ApplyBlock(1, tx1); err != nil {
		t.Fatalf("重複同步應冪等: %v", err)
	}
	tx2 := []chaindb.Transaction{{
		TxHash: "t1", FromAddr: "alice", ToAddr: "bob", Amount: "1.5", Fee: "0.03",
	}}
	// 付款人需有餘額（鏈上已驗證；此處先充值使同步成立）。
	if err := svc.Deposit("alice", AssetTACm, amt("5", AssetTACm), "fund"); err != nil {
		t.Fatal(err)
	}
	if err := svc.ApplyBlock(2, tx2); err != nil {
		t.Fatal(err)
	}
	// 跳號拒絕。
	if err := svc.ApplyBlock(4, nil); err == nil {
		t.Fatal("跳號同步應報錯")
	}
	miner, _ := svc.Balance("miner1")
	// M58：coinbase 收款人在鏈上瓜分時已確定（出塊者只把份額寫給開機且在線的礦工），
	// 錢包層照單入帳，不再本地判斷在線 → 全網帳本一致。
	if miner.TACmBalance.Cmp(bigS("10000000000000000000")) != 0 {
		t.Fatalf("miner1=%s want 10 TACM (coinbase 照單入帳)", miner.TACmBalance)
	}
	pool, _ := svc.Balance(RewardPoolAddr)
	if pool.TACmBalance.Sign() != 0 {
		t.Fatalf("reward_pool=%s want 0 (無 pool coinbase)", pool.TACmBalance)
	}
	alice, _ := svc.Balance("alice")
	// 5 - (1.5+0.03) = 3.47。
	if alice.TACmBalance.Cmp(bigS("3470000000000000000")) != 0 {
		t.Fatalf("alice=%s want 3.47 TACM", alice.TACmBalance)
	}
	bob, _ := svc.Balance("bob")
	if bob.TACmBalance.Cmp(bigS("1500000000000000000")) != 0 {
		t.Fatalf("bob=%s want 1.5 TACM", bob.TACmBalance)
	}
}
