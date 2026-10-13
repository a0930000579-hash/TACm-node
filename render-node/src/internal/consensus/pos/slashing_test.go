package pos

import (
	"math"
	"testing"
)

// TestRegisterValidatorPubkey：註冊必須帶公鑰，重複地址拒絕。
func TestRegisterValidatorPubkey(t *testing.T) {
	s := openTestStore(t, DefaultConfig())
	if _, err := s.RegisterValidator(1, "tx0val1", "node1", 0.05, ""); err == nil {
		t.Error("空公鑰應報錯")
	}
	v, err := s.RegisterValidator(1, "tx0val1", "node1", 0.05, "ab12cd34")
	if err != nil {
		t.Fatal(err)
	}
	if v.PubkeyHex != "ab12cd34" || v.Status != "active" {
		t.Errorf("註冊結果錯誤: %+v", v)
	}
	if _, err := s.RegisterValidator(2, "tx0val1", "dup", 0.05, "ef56ab78"); err == nil {
		t.Error("重複地址應報錯")
	}
}

// TestJailAndSlash：監禁→罰沒→證據→解除監禁全流程。
func TestJailAndSlash(t *testing.T) {
	s := openTestStore(t, DefaultConfig())
	if _, err := s.RegisterValidator(1, "tx0val1", "node1", 0.05, "ab12cd34"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stake(1, "tx0val1", 1000, nil); err != nil {
		t.Fatal(err)
	}

	// 監禁
	jv, err := s.JailValidator("tx0val1", "equivocation")
	if err != nil {
		t.Fatal(err)
	}
	if jv.Status != "jailed" || jv.JailedAt == nil {
		t.Errorf("監禁狀態錯誤: %+v", jv)
	}
	// 監禁後不在共識集
	active, _ := s.GetActiveValidatorsForConsensus(21)
	if len(active) != 0 {
		t.Errorf("監禁後仍出現在活躍集: %+v", active)
	}

	// 罰沒 5%
	ev, err := s.SlashValidator("tx0val1", 0.05, "double-vote")
	if err != nil {
		t.Fatal(err)
	}
	if !approx(ev.Amount, 50.0, 1e-6) {
		t.Errorf("罰沒額 = %g, want 50", ev.Amount)
	}
	events, _ := s.GetSlashEvents(10)
	if len(events) != 1 || events[0].Address != "tx0val1" {
		t.Errorf("證據記錄錯誤: %+v", events)
	}
	v, _ := s.byAddress("tx0val1")
	if !approx(v.TotalStake, 950.0, 1e-6) {
		t.Errorf("罰沒後總質押 = %g, want 950", v.TotalStake)
	}

	// 解除監禁
	uv, err := s.UnjailValidator("tx0val1")
	if err != nil {
		t.Fatal(err)
	}
	if uv.Status != "active" {
		t.Errorf("解除監禁失敗: %+v", uv)
	}
}

// TestActiveValidatorsOrder：共識集按質押降序，權重正比。
func TestActiveValidatorsOrder(t *testing.T) {
	s := openTestStore(t, DefaultConfig())
	for _, tc := range []struct {
		uid  int64
		addr string
		amt  float64
	}{
		{1, "tx0vA", 500}, {2, "tx0vB", 2000}, {3, "tx0vC", 1000},
	} {
		if _, err := s.RegisterValidator(tc.uid, tc.addr, tc.addr, 0.05, "ab12cd34"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Stake(tc.uid, tc.addr, tc.amt, nil); err != nil {
			t.Fatal(err)
		}
	}
	active, err := s.GetActiveValidatorsForConsensus(21)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 3 {
		t.Fatalf("活躍驗證人 = %d, want 3", len(active))
	}
	if active[0].Address != "tx0vB" || active[2].Address != "tx0vA" {
		t.Errorf("排序錯誤: %v %v %v", active[0].Address, active[1].Address, active[2].Address)
	}
	// 權重近似正比：B/C/A = 2000/1000/500
	pow := func(stake float64) int { return int(math.Ceil(stake / 100)) }
	if pow(active[0].TotalStake) != 20 || pow(active[1].TotalStake) != 10 || pow(active[2].TotalStake) != 5 {
		t.Errorf("權重計算錯誤: %d %d %d", pow(active[0].TotalStake), pow(active[1].TotalStake), pow(active[2].TotalStake))
	}
}
