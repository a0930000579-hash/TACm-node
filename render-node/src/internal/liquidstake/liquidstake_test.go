package liquidstake

import (
	"math"
	"path/filepath"
	"testing"
)

func newLS(t *testing.T) *LiquidStake {
	t.Helper()
	ls, err := New(filepath.Join(t.TempDir(), "ls.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = ls.Close() })
	return ls
}

func TestDepositMintRate(t *testing.T) {
	ls := newLS(t)
	m, err := ls.Deposit("0xabc", 1000)
	if err != nil {
		t.Fatalf("Deposit: %v", err)
	}
	if math.Abs(m-1000) > 1e-6 { // 初期 1:1
		t.Fatalf("mint = %.4f, want 1000", m)
	}
	s := ls.Stats()
	if s.TotalStaked != 1000 || s.TotalStTACm != 1000 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestExchangeRateGrowsWithRewards(t *testing.T) {
	ls := newLS(t)
	_, _ = ls.Deposit("0xabc", 1000)
	if err := ls.AddRewards(100); err != nil { // 10% 獎勵
		t.Fatalf("AddRewards: %v", err)
	}
	rate := ls.ExchangeRate()
	if math.Abs(rate-1.1) > 1e-9 {
		t.Fatalf("rate = %.4f, want 1.1", rate)
	}
	_, redeemable, err := ls.Balance("0xabc")
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if math.Abs(redeemable-1100) > 1e-6 {
		t.Fatalf("redeemable = %.2f, want 1100", redeemable)
	}
}

func TestRedeem(t *testing.T) {
	ls := newLS(t)
	_, _ = ls.Deposit("0xabc", 1000)
	_ = ls.AddRewards(100)
	out, err := ls.Redeem("0xabc", 1000)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if math.Abs(out-1100) > 1e-6 {
		t.Fatalf("redeem out = %.2f, want 1100", out)
	}
	s := ls.Stats()
	if s.TotalStTACm != 0 || s.TotalStaked != 0 {
		t.Fatalf("stats after redeem = %+v", s)
	}
}

func TestInsufficient(t *testing.T) {
	ls := newLS(t)
	_, _ = ls.Deposit("0xabc", 100)
	if _, err := ls.Redeem("0xabc", 999); err != ErrInsufficient {
		t.Fatalf("err = %v, want ErrInsufficient", err)
	}
	if _, _, err := ls.Balance("0xzzz"); err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// v75：PoolShare——池在節點委託分潤中的份額。
func TestLiquidStakePoolShare(t *testing.T) {
	dir := t.TempDir()
	l, err := New(filepath.Join(dir, "ls.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	// 無池質押：份額 0
	if got := l.PoolShare(1000, 100); got != 0 {
		t.Fatalf("no stake: got %v want 0", got)
	}
	// 池質押 1000、節點自質押 4000：份額 = 100 * 1000/5000 = 20
	if _, err := l.Deposit("0xAlice", 1000); err != nil {
		t.Fatal(err)
	}
	if got := l.PoolShare(4000, 100); got < 19.99 || got > 20.01 {
		t.Fatalf("share: got %v want 20", got)
	}
	// nodeShare 0：份額 0
	if got := l.PoolShare(4000, 0); got != 0 {
		t.Fatalf("zero nodeShare: got %v want 0", got)
	}
	// 節點自質押 0：份額 0（避免除零）
	if got := l.PoolShare(0, 100); got != 0 {
		t.Fatalf("zero self: got %v want 0", got)
	}
	// 注入後匯率上升：rewardReserve 500 → rate 1.05（既有 AddRewards 行為）
	if err := l.AddRewards(50); err != nil {
		t.Fatal(err)
	}
	st := l.Stats()
	if st.ExchangeRate < 1.049 || st.ExchangeRate > 1.051 {
		t.Fatalf("rate: got %v want 1.05", st.ExchangeRate)
	}
}


// TestRateHistory v76：後端持久化匯率歷史（deposit/reward 後寫點、查詢升序）。
func TestRateHistory(t *testing.T) {
	ls := newLS(t)
	defer ls.Close()
	if _, err := ls.Deposit("tx0hist1", 5000); err != nil {
		t.Fatalf("deposit: %v", err)
	}
	if err := ls.AddRewards(250); err != nil {
		t.Fatalf("addrewards: %v", err)
	}
	if _, err := ls.Deposit("tx0hist2", 1000); err != nil {
		t.Fatalf("deposit2: %v", err)
	}
	pts, err := ls.RateHistory(10)
	if err != nil {
		t.Fatalf("ratehistory: %v", err)
	}
	if len(pts) < 3 {
		t.Fatalf("預期 ≥3 個歷史點，得 %d", len(pts))
	}
	for i := 1; i < len(pts); i++ {
		if pts[i].TS < pts[i-1].TS {
			t.Fatalf("歷史點未升序: %d < %d", pts[i].TS, pts[i-1].TS)
		}
		if pts[i].Rate < pts[i-1].Rate {
			t.Fatalf("匯率應遞增（獎勵注入後），得 %f → %f", pts[i-1].Rate, pts[i].Rate)
		}
	}
}
