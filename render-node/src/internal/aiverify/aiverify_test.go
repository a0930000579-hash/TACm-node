package aiverify

// aiverify_test.go — 純狀態層測試（t.TempDir 隔離 SQLite）：
//  A) 三驗證者 2 approve / 1 reject → verified；正確兩者平分獎勵、少數 rejecter 被 slash。
//  B) 錯誤路徑：重複投票、非驗證者、任務不存在、已結案。
//  C) 投票不足門檻 → pending；過挑戰窗口 → expired，submitter 退費。

import (
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	// 注入可控時鐘。
	clock := int64(1_700_000_000)
	s.nowFn = func() int64 { return clock }
	s.params.ChallengeWindowS = 60 // 測試用短窗口
	return s
}

func TestSettleVerifiedAndSlash(t *testing.T) {
	s := newTestStore(t)

	v0, v1, v2 := "0xaaa0000000000000000000000000000000000001",
		"0xbbb0000000000000000000000000000000000002",
		"0xccc0000000000000000000000000000000000003"
	for _, a := range []string{v0, v1, v2} {
		if _, err := s.RegisterVerifier(a, 10); err != nil {
			t.Fatalf("Register %s: %v", a, err)
		}
	}

	task, err := s.SubmitTask("BSC", 42, "v1", "QmInput", "inHash", "0xSUB", "wantOut", 9)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if task.Status != StatusPending {
		t.Fatalf("初始應 pending, got %s", task.Status)
	}

	// 前兩票皆 approve，未達 3 票 → 不結算。
	r0, err := s.Attest(task.Id, v0, "approve", "", "sig0")
	if err != nil {
		t.Fatalf("attest v0: %v", err)
	}
	if r0 != nil {
		t.Fatalf("第 1 票不應結算: %+v", r0)
	}
	r1, err := s.Attest(task.Id, v1, "approve", "", "sig1")
	if err != nil {
		t.Fatalf("attest v1: %v", err)
	}
	if r1 != nil {
		t.Fatalf("第 2 票不應結算: %+v", r1)
	}
	got, _ := s.GetTask(task.Id)
	if got.Status != StatusPending {
		t.Fatalf("2 票後應仍 pending, got %s", got.Status)
	}

	// 第三票 reject（主張錯誤 output）→ approve 2/3=66%>50% → verified。
	r2, err := s.Attest(task.Id, v2, "reject", "fakeOut", "sig2")
	if err != nil {
		t.Fatalf("attest v2: %v", err)
	}
	if r2 == nil {
		t.Fatalf("第 3 票應觸發結算")
	}
	if r2.Status != StatusVerified {
		t.Fatalf("應 verified, got %s", r2.Status)
	}
	if r2.ConsensusHash != "wantOut" {
		t.Fatalf("共識 hash 應 wantOut, got %q", r2.ConsensusHash)
	}
	// 獎勵：v0/v1 平分 9 → 各 4.5。
	if len(r2.Rewards) != 2 {
		t.Fatalf("應 2 名正確者, got %d", len(r2.Rewards))
	}
	for _, rw := range r2.Rewards {
		if rw.Amount != 4.5 {
			t.Fatalf("每人應得 4.5, got %+v", rw)
		}
	}
	// Slash：v2 沒收 10% × 10 = 1.0。
	if len(r2.Slashes) != 1 || r2.Slashes[0].Verifier != v2 {
		t.Fatalf("應 slash v2: %+v", r2.Slashes)
	}
	if r2.Slashes[0].Slash != 1.0 {
		t.Fatalf("v2 應被沒收 1.0, got %+v", r2.Slashes[0])
	}

	// 狀態落庫檢查。
	got, _ = s.GetTask(task.Id)
	if got.Status != StatusVerified {
		t.Fatalf("任務應 verified, got %s", got.Status)
	}
	v2g, _ := s.GetVerifier(v2)
	if v2g.Stake != 9.0 {
		t.Fatalf("v2 slash 後質押應 9.0, got %v", v2g.Stake)
	}
	if v2g.SlashedCount != 1 {
		t.Fatalf("v2 slashed_count 應 1, got %d", v2g.SlashedCount)
	}
	v0g, _ := s.GetVerifier(v0)
	if v0g.Reputation != 1 {
		t.Fatalf("v0 reputation 應 1, got %d", v0g.Reputation)
	}
}

func TestAttestErrors(t *testing.T) {
	s := newTestStore(t)
	v0, rogue := "0xaaa0000000000000000000000000000000000001", "0xeee0000000000000000000000000000000000009"
	if _, err := s.RegisterVerifier(v0, 10); err != nil {
		t.Fatalf("Register: %v", err)
	}
	task, _ := s.SubmitTask("TACm", 1, "v1", "QmI", "", "0xSUB", "wantOut", 5)

	// 非驗證者投票 → 錯。
	if _, err := s.Attest(task.Id, rogue, "approve", "", ""); err == nil {
		t.Fatalf("非驗證者應被拒絕")
	}
	// 任務不存在。
	if _, err := s.Attest(99999, v0, "approve", "", ""); err == nil {
		t.Fatalf("不存在任務應報錯")
	}
	// 合法第一票。
	if _, err := s.Attest(task.Id, v0, "approve", "", ""); err != nil {
		t.Fatalf("首票應成功: %v", err)
	}
	// 重複投票（雙花）→ 錯。
	if _, err := s.Attest(task.Id, v0, "approve", "", ""); err == nil {
		t.Fatalf("重複投票應被拒絕")
	}
	// 非法決議。
	if _, err := s.Attest(task.Id, rogue, "maybe", "", ""); err == nil {
		t.Fatalf("非法決議應被拒絕")
	}
}

func TestExpireBelowQuorum(t *testing.T) {
	s := newTestStore(t)
	v0, v1 := "0xaaa0000000000000000000000000000000000001",
		"0xbbb0000000000000000000000000000000000002"
	for _, a := range []string{v0, v1} {
		if _, err := s.RegisterVerifier(a, 10); err != nil {
			t.Fatalf("Register: %v", err)
		}
	}
	task, _ := s.SubmitTask("TACm", 1, "v1", "QmI", "", "0xSUB", "wantOut", 7)

	// 只投 2 票（MinVerifiers=3）→ 不足門檻，pending。
	if _, err := s.Attest(task.Id, v0, "approve", "", ""); err != nil {
		t.Fatalf("attest v0: %v", err)
	}
	if _, err := s.Attest(task.Id, v1, "approve", "", ""); err != nil {
		t.Fatalf("attest v1: %v", err)
	}
	got, _ := s.GetTask(task.Id)
	if got.Status != StatusPending {
		t.Fatalf("不足門檻應 pending, got %s", got.Status)
	}

	// 推進時鐘越過挑戰窗口。
	s.nowFn = func() int64 { return 1_700_000_000 + 61 }
	expired, err := s.ExpirePending()
	if err != nil {
		t.Fatalf("ExpirePending: %v", err)
	}
	if len(expired) != 1 || expired[0].TaskId != task.Id {
		t.Fatalf("應過期 1 筆: %+v", expired)
	}
	if expired[0].SubmitterRefund != 7 {
		t.Fatalf("應退費 7, got %v", expired[0].SubmitterRefund)
	}
	got, _ = s.GetTask(task.Id)
	if got.Status != StatusExpired {
		t.Fatalf("應 expired, got %s", got.Status)
	}
}
