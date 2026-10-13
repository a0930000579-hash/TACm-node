package dex

import (
	"math/big"
	"testing"
)

// newStakeTestEngine 建立含農場支援的測試引擎（fakeLedger 記帳）。
func newStakeTestEngine(t *testing.T) (*Engine, *fakeLedger) {
	t.Helper()
	admin := "tx0admin"
	ledger := newFakeLedger([]string{"TKA", "TKB"}, nil)
	ledger.bal[admin] = map[string]*big.Int{}
	// 1e24 raw（1,000,000 顆 × 1e18）。
	ledger.bal[admin]["TKA"], _ = new(big.Int).SetString("1000000000000000000000000", 10)
	ledger.bal[admin]["TKB"], _ = new(big.Int).SetString("500000000000000000000000", 10)
	eng, err := New(t.TempDir(), admin,
		func(poolID, from, to, contract string, amount *big.Int) error {
			// 金庫標記 → 金庫帳戶字串（測試層直接當持有人；from/to 雙向）。
			if from == "tac-dex-stake:"+poolID {
				from = "vault:" + poolID
			}
			if to == "tac-dex-stake:"+poolID {
				to = "vault:" + poolID
			}
			return ledger.transfer(from, to, contract, amount)
		},
		func(holder, contract string) (*big.Int, error) {
			if ledger.bal[holder] == nil || ledger.bal[holder][contract] == nil {
				return big.NewInt(0), nil
			}
			return new(big.Int).Set(ledger.bal[holder][contract]), nil
		})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng, ledger
}

// TestStakeCreateFund 建農場／重複拒絕／注入資金。
func TestStakeCreateFund(t *testing.T) {
	eng, _ := newStakeTestEngine(t)
	pool, err := eng.CreatePool("TKA", "TKB", "tx0pool")
	if err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	sp, err := eng.CreateStake(pool.ID, "TKA", big.NewInt(10))
	if err != nil {
		t.Fatalf("CreateStake: %v", err)
	}
	if sp.RewardToken != "TKA" || sp.RewardPerBlock.Cmp(big.NewInt(10)) != 0 {
		t.Fatalf("農場欄位異常: %+v", sp)
	}
	if _, err := eng.CreateStake(pool.ID, "TKA", big.NewInt(10)); err != ErrStakeExists {
		t.Fatalf("重複建農場應拒絕，得 %v", err)
	}
	funded, err := eng.FundStake(pool.ID, big.NewInt(1000))
	if err != nil {
		t.Fatalf("FundStake: %v", err)
	}
	if funded.Funded.Cmp(big.NewInt(1000)) != 0 {
		t.Fatalf("funded 應為 1000，得 %v", funded.Funded)
	}
}

// TestStakeEarnAndClaim 質押後按塊累積獎勵並可領取（鏈上轉帳）。
func TestStakeEarnAndClaim(t *testing.T) {
	eng, ledger := newStakeTestEngine(t)
	admin := "tx0admin"
	pool, err := eng.CreatePool("TKA", "TKB", "tx0pool")
	if err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	// admin 注入流動性 100000/50000 → LP=707。
	if _, err := eng.AddLiquidity(pool.ID, admin, big.NewInt(100000), big.NewInt(50000)); err != nil {
		t.Fatalf("AddLiquidity: %v", err)
	}
	if _, err := eng.CreateStake(pool.ID, "TKA", big.NewInt(1)); err != nil {
		t.Fatalf("CreateStake: %v", err)
	}
	fund, _ := new(big.Int).SetString("10000000000000000000", 10) // 10 顆（1e19 raw）
	if _, err := eng.FundStake(pool.ID, fund); err != nil {
		t.Fatalf("FundStake: %v", err)
	}
	// 質押全部 70710 份額（LP=√(100000·50000)=70710）。
	if _, err := eng.Stake(pool.ID, admin, big.NewInt(70710)); err != nil {
		t.Fatalf("Stake: %v", err)
	}
	// 出 10 塊：acc = 1*10*1e18/70710；pending ≈ 10 顆（raw 值，÷1e18 定點）。
	// 整數除法捨入，容許 <2 誤差。
	if err := eng.StakeTick(10); err != nil {
		t.Fatalf("StakeTick: %v", err)
	}
	pending, err := eng.Pending(pool.ID, admin)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	want := big.NewInt(10)
	diff := new(big.Int).Sub(new(big.Int).Set(want), pending)
	diff.Abs(diff)
	if diff.Cmp(big.NewInt(2)) > 0 {
		t.Fatalf("pending 應接近 %s，得 %s", want, pending)
	}
	// 領取：金庫扣、admin 收。
	before := ledger.bal[admin]["TKA"]
	claimed, err := eng.Claim(pool.ID, admin)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	diffC := new(big.Int).Sub(new(big.Int).Set(want), claimed)
	diffC.Abs(diffC)
	if diffC.Cmp(big.NewInt(1e5)) > 0 {
		t.Fatalf("claimed 應接近 %s，得 %s", want, claimed)
	}
	after := ledger.bal[admin]["TKA"]
	gain := new(big.Int).Sub(after, before)
	diffG := new(big.Int).Sub(new(big.Int).Set(want), gain)
	diffG.Abs(diffG)
	if diffG.Cmp(big.NewInt(1e5)) > 0 {
		t.Fatalf("admin 應增加約 %s，得 %s", want, gain)
	}
	// 再次領取應為 0（已結算）。
	if _, err := eng.Claim(pool.ID, admin); err == nil {
		t.Fatalf("二次領取應拒絕")
	}
}

// TestStakeLockAndUnstake 質押份額不可退出；解除後可退。
func TestStakeLockAndUnstake(t *testing.T) {
	eng, _ := newStakeTestEngine(t)
	admin := "tx0admin"
	pool, err := eng.CreatePool("TKA", "TKB", "tx0pool")
	if err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	if _, err := eng.AddLiquidity(pool.ID, admin, big.NewInt(100000), big.NewInt(50000)); err != nil {
		t.Fatalf("AddLiquidity: %v", err)
	}
	if _, err := eng.CreateStake(pool.ID, "TKA", big.NewInt(10)); err != nil {
		t.Fatalf("CreateStake: %v", err)
	}
	if _, err := eng.Stake(pool.ID, admin, big.NewInt(70710)); err != nil {
		t.Fatalf("Stake: %v", err)
	}
	// 全額退出被鎖（已質押）。
	if _, err := eng.RemoveLiquidity(pool.ID, admin, big.NewInt(70710)); err == nil {
		t.Fatalf("質押中退出應拒絕")
	}
	// 解除後可全額退出。
	if _, err := eng.Unstake(pool.ID, admin, big.NewInt(70710)); err != nil {
		t.Fatalf("Unstake: %v", err)
	}
	pos, err := eng.PositionOf(pool.ID, admin)
	if err != nil {
		t.Fatalf("PositionOf: %v", err)
	}
	if pos.Shares.Cmp(big.NewInt(70710)) != 0 {
		t.Fatalf("解除後份額應為 70710，得 %v", pos.Shares)
	}
	if _, err := eng.RemoveLiquidity(pool.ID, admin, big.NewInt(70710)); err != nil {
		t.Fatalf("解除後退出應成功: %v", err)
	}
}
