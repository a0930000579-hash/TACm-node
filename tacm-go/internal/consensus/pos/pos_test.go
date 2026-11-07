package pos

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T, cfg Config) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pos.db")
	s, err := Open(path, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func approx(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

func TestCalculateReward(t *testing.T) {
	got := CalculateReward(1000, 2592000, 0.12) // 30 天
	if !approx(got, 9.8630137, 1e-6) {
		t.Errorf("30天收益 = %.8f, want ~9.8630137", got)
	}
}

func TestStakeAndAppend(t *testing.T) {
	s := openTestStore(t, DefaultConfig())

	if _, err := s.Stake(1, "tx0aaa", 50, nil); err == nil {
		t.Error("低於最小質押應報錯")
	}
	st, err := s.Stake(1, "tx0aaa", 1000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "active" || st.Amount != 1000 {
		t.Errorf("質押錯誤: %+v", st)
	}
	stakes, _ := s.GetUserStakes(1)
	if len(stakes) != 1 {
		t.Errorf("質押數 = %d, want 1", len(stakes))
	}
	// 追加
	st2, err := s.Stake(1, "tx0aaa", 500, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st2.Amount != 1500 || st2.ID != st.ID {
		t.Error("追加質押應合併到同一記錄，金額 1500")
	}
	stakes, _ = s.GetUserStakes(1)
	if len(stakes) != 1 {
		t.Error("追加後仍應只有一條")
	}
}

func TestUnlockFlow(t *testing.T) {
	cfg := DefaultConfig()
	cfg.UnlockPeriod = 1
	s := openTestStore(t, cfg)

	st, _ := s.Stake(1, "tx0aaa", 1000, nil)
	req, err := s.RequestUnlock(1, st.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteUnlock(req.ID); err == nil {
		t.Error("解鎖期未結束應報錯")
	}
	time.Sleep(1200 * time.Millisecond)
	amount, err := s.CompleteUnlock(req.ID)
	if err != nil {
		t.Fatalf("到期應完成解鎖: %v", err)
	}
	if amount != 1000 {
		t.Errorf("退回金額 = %f, want 1000", amount)
	}
	stInfo, _ := s.GetStats()
	if stInfo.TotalStaked != 0 {
		t.Errorf("解鎖後全網質押應為 0, got %f", stInfo.TotalStaked)
	}
}

func TestValidator(t *testing.T) {
	s := openTestStore(t, DefaultConfig())

	v, err := s.RegisterValidator(1, "tx0val1", "node1", 0.05, "ab12cd34")
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != "active" {
		t.Error("驗證人應為 active")
	}
	if _, err := s.RegisterValidator(1, "tx0val1", "dup", 0.05, "ef56ab78"); err == nil {
		t.Error("同地址重複註冊應報錯")
	}
	if _, err := s.RegisterValidator(2, "tx0val2", "high", 0.3, "cd34ef56"); err == nil {
		t.Error("傭金率過高應報錯")
	}
	vals, _ := s.GetValidators(10)
	if len(vals) != 1 {
		t.Errorf("驗證人數 = %d, want 1", len(vals))
	}
}

func TestDistributeRewards(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RewardInterval = 1
	s := openTestStore(t, cfg)

	s.Stake(1, "tx0aaa", 1000, nil)
	r1, err := s.DistributeRewards()
	if err != nil || !r1.Skipped {
		t.Error("首次應初始化並跳過")
	}
	time.Sleep(1200 * time.Millisecond)
	r2, err := s.DistributeRewards()
	if err != nil {
		t.Fatal(err)
	}
	if r2.Skipped || r2.Distributed <= 0 {
		t.Errorf("第二次應分配收益: %+v", r2)
	}
	earned, _ := s.GetUserTotalEarned(1)
	if earned <= 0 {
		t.Error("累計收益應 > 0")
	}
}

func TestWeights(t *testing.T) {
	s := openTestStore(t, DefaultConfig())
	s.Stake(1, "tx0aaa", 400, nil)
	s.Stake(2, "tx0bbb", 600, nil)

	wA, err := s.GetPosWeight("tx0aaa")
	if err != nil {
		t.Fatal(err)
	}
	if !approx(wA, 0.4, 1e-6) {
		t.Errorf("A 的 PoS 權重 = %f, want 0.4", wA)
	}
	cw, _ := s.GetConsensusWeights("tx0aaa", 0)
	// posW 0.4 → 0.16；pow 0.3；time 0 → total 0.46
	if !approx(cw.TotalWeight, 0.46, 1e-6) {
		t.Errorf("三元總權重 = %f, want 0.46", cw.TotalWeight)
	}
	// 在線滿一年 → time 0.3
	cw2, _ := s.GetConsensusWeights("tx0aaa", yearSeconds)
	if !approx(cw2.TimeWeight, 0.3, 1e-6) {
		t.Errorf("時間權重 = %f, want 0.3", cw2.TimeWeight)
	}
}

func TestStats(t *testing.T) {
	s := openTestStore(t, DefaultConfig())
	s.Stake(1, "tx0aaa", 1000, nil)
	st, err := s.GetStats()
	if err != nil {
		t.Fatal(err)
	}
	if st.TotalStaked != 1000 || st.TotalStakers != 1 {
		t.Errorf("統計錯誤: %+v", st)
	}
}
