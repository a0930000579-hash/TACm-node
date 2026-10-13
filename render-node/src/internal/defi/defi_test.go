package defi

import (
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite" // 註冊 sqlite driver（測試直接開啟資料庫用）
)

func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir()))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestSeedPools(t *testing.T) {
	st := openTest(t)
	pools, err := st.Pools()
	if err != nil {
		t.Fatalf("Pools: %v", err)
	}
	if len(pools) != 3 {
		t.Fatalf("期望 3 池，得 %d", len(pools))
	}
	if pools[0].Pair != "TACM/USDT" || pools[0].Reserve0 != 50000 || pools[0].APR <= 0 {
		t.Fatalf("池1 seed 異常: %+v", pools[0])
	}
	markets, err := st.LendingMarkets()
	if err != nil {
		t.Fatalf("LendingMarkets: %v", err)
	}
	if len(markets) != 3 || markets[0].Asset != "USDT" || markets[0].CollateralFactor != 0.75 {
		t.Fatalf("借貸市場 seed 異常: %+v", markets)
	}
}

func TestAddRemoveLiquidity(t *testing.T) {
	st := openTest(t)
	lp, err := st.AddLiquidity("tx0A", 1, 10, 100)
	if err != nil {
		t.Fatalf("AddLiquidity: %v", err)
	}
	if lp <= 0 {
		t.Fatalf("LP 應 >0，得 %v", lp)
	}
	pos, err := st.LPForUser("tx0A", 1)
	if err != nil || pos == nil {
		t.Fatalf("LPForUser: %v %v", pos, err)
	}
	if pos.LPAmount != lp || pos.Token0Amount != 10 || pos.Token1Amount != 100 {
		t.Fatalf("頭寸異常: %+v", pos)
	}
	pool, _ := st.GetPool(1)
	if pool.Reserve0 != 50010 || pool.Reserve1 != 500100 {
		t.Fatalf("池儲備未更新: %+v", pool)
	}
	// 移除全部：贖回等於投入。
	a0, a1, _, err := st.RemoveLiquidity("tx0A", pos.ID, lp)
	if err != nil {
		t.Fatalf("RemoveLiquidity: %v", err)
	}
	if a0 < 9.999 || a0 > 10.001 || a1 < 99.99 || a1 > 100.01 {
		t.Fatalf("贖回金額異常: %v %v", a0, a1)
	}
	pool, _ = st.GetPool(1)
	if pool.Reserve0 != 50000 || pool.Reserve1 != 500000 {
		t.Fatalf("池儲備未還原: %+v", pool)
	}
	if p, _ := st.LPForUser("tx0A", 1); p != nil {
		t.Fatalf("頭寸應已刪除: %+v", p)
	}
}

func TestClaimRewardAndConservation(t *testing.T) {
	st := openTest(t)
	_, err := st.AddLiquidity("tx0A", 2, 10, 100)
	if err != nil {
		t.Fatalf("AddLiquidity: %v", err)
	}
	pos, _ := st.LPForUser("tx0A", 2)
	// 獎勵按秒級時間差計算：等待跨秒。
	time.Sleep(1100 * time.Millisecond)
	reward, token, err := st.ClaimLPReward("tx0A", pos.ID)
	if err != nil {
		t.Fatalf("ClaimLPReward: %v", err)
	}
	if token != "TACm" {
		t.Fatalf("獎勵代幣應為 TACm，得 %s", token)
	}
	if reward <= 0 {
		t.Fatalf("獎勵應 >0，得 %v", reward)
	}
	// 立即再領應為「暫無獎勵」（last_reward_at 已更新）。
	if _, _, err := st.ClaimLPReward("tx0A", pos.ID); err == nil {
		t.Fatalf("重複領取應失敗")
	}
}

func TestBorrowRepayLending(t *testing.T) {
	st := openTest(t)
	if err := st.DepositLending("tx0A", "USDT", 100); err != nil {
		t.Fatalf("DepositLending: %v", err)
	}
	deps, _ := st.MyDeposits("tx0A")
	if len(deps) != 1 || deps[0].Amount != 100 {
		t.Fatalf("存款異常: %+v", deps)
	}
	// 抵押 100 TACM 借 60 USDT（factor 0.75 → 上限 75）。
	id, err := st.Borrow("tx0A", "TACm", 100, "USDT", 60)
	if err != nil {
		t.Fatalf("Borrow: %v", err)
	}
	if _, err := st.Borrow("tx0A", "TACm", 100, "USDT", 80); err == nil {
		t.Fatalf("超額借款應被拒")
	}
	loans, _ := st.MyLoans("tx0A")
	if len(loans) != 1 || loans[0].Status != "active" || loans[0].LiquidationPrice <= 0 {
		t.Fatalf("貸款異常: %+v", loans)
	}
	// 全額還款：本金+利息 → repaid、抵押釋放金額。
	res, err := st.RepayLoan("tx0A", id, 60.01)
	if err != nil {
		t.Fatalf("RepayLoan: %v", err)
	}
	if res["status"] != 1 || res["collateral_returned"] != 100 {
		t.Fatalf("還款結果異常: %+v", res)
	}
	loans, _ = st.MyLoans("tx0A")
	if loans[0].Status != "repaid" {
		t.Fatalf("貸款應已結清: %+v", loans[0])
	}
	// 取款 50：餘額不足場景。
	if err := st.WithdrawLending("tx0A", "USDT", 50); err != nil {
		t.Fatalf("WithdrawLending: %v", err)
	}
	if err := st.WithdrawLending("tx0A", "USDT", 51); err == nil {
		t.Fatalf("超額取款應被拒")
	}
}

func TestIDOSubscribeClaim(t *testing.T) {
	st := openTest(t)
	// 測試資料庫以預設時間窗（+1 天）seed → 直接改 start_time 讓項目可認購。
	if _, err := st.db.Exec(`UPDATE defi_ido_projects SET start_time=?`, time.Now().Unix()-10); err != nil {
		t.Fatalf("改 start: %v", err)
	}
	if _, err := st.db.Exec(`UPDATE defi_ido_projects SET status='completed' WHERE id=1`); err != nil {
		t.Fatalf("改 status: %v", err)
	}
	tokens, err := st.SubscribeIDO("tx0A", 1, 100)
	if err != nil {
		t.Fatalf("SubscribeIDO: %v", err)
	}
	if tokens != 200 {
		t.Fatalf("100/0.5 應為 200 枚，得 %v", tokens)
	}
	if _, err := st.SubscribeIDO("tx0A", 1, 100); err == nil {
		t.Fatalf("重複認購應拒")
	}
	if _, err := st.SubscribeIDO("tx0A", 1, 10); err == nil {
		t.Fatalf("低於 min_buy 應拒")
	}
	got, symbol, err := st.ClaimIDOTokens("tx0A", 1)
	if err != nil {
		t.Fatalf("ClaimIDOTokens: %v", err)
	}
	if got != 200 || symbol != "TACG" {
		t.Fatalf("領取異常: %v %v", got, symbol)
	}
	if _, _, err := st.ClaimIDOTokens("tx0A", 1); err == nil {
		t.Fatalf("重複領取應拒")
	}
	proj, _ := st.GetIDOProject(1)
	if proj.TotalRaised != 100 || proj.TotalParticipants != 1 {
		t.Fatalf("募集記帳異常: %+v", proj)
	}
}

func TestVaultDepositCompoundWithdraw(t *testing.T) {
	st := openTest(t)
	st.CompoundMinSec = 2
	shares, err := st.DepositVault("tx0A", 1, 100)
	if err != nil {
		t.Fatalf("DepositVault: %v", err)
	}
	if shares != 100 {
		t.Fatalf("首存份額應等額，得 %v", shares)
	}
	if _, err := st.DepositVault("tx0A", 1, 1); err == nil {
		t.Fatalf("低於 min_deposit 應拒")
	}
	// 第二筆按比例式（總量 100100/100100 → 1:1）。
	shares2, err := st.DepositVault("tx0A", 1, 100)
	if err != nil {
		t.Fatalf("二次存入: %v", err)
	}
	if shares2 < 99.99 || shares2 > 100.01 {
		t.Fatalf("比例式份額異常: %v", shares2)
	}
	pos, _ := st.MyVaults("tx0A")
	if len(pos) != 1 || pos[0].Shares < 199.99 || pos[0].Shares > 200.01 {
		t.Fatalf("合併頭寸異常: %+v", pos)
	}
	// 間隔不足拒。
	time.Sleep(2100 * time.Millisecond)
	yield, ns, err := st.CompoundVault("tx0A", 1)
	if err != nil {
		t.Fatalf("CompoundVault: %v", err)
	}
	if yield <= 0 || ns <= 0 {
		t.Fatalf("復投異常: %v %v", yield, ns)
	}
	v, _ := st.GetVault(1)
	if v.TotalAssets <= 100150 || v.TotalShares <= 100150 {
		t.Fatalf("復投後總量未增: %+v", v)
	}
	// 全額取出 → 頭寸刪除、贖回 ≈ 本金＋收益。
	pos2, _ := st.MyVaults("tx0A")
	amt, err := st.WithdrawVault("tx0A", float64(pos2[0].ID), pos2[0].Shares)
	if err != nil {
		t.Fatalf("WithdrawVault: %v", err)
	}
	if amt < 200 {
		t.Fatalf("贖回應含收益 >200，得 %v", amt)
	}
	if p, _ := st.MyVaults("tx0A"); len(p) != 0 {
		t.Fatalf("頭寸應清空: %+v", p)
	}
}
