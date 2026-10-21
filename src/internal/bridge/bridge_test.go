package bridge

import (
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"

	"tacm/internal/crypto"
)

func newTestBridge(t *testing.T) *Bridge {
	t.Helper()
	b, err := New(DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestLockMintFlow(t *testing.T) {
	b := newTestBridge(t)
	defer b.Close()

	res, err := b.LockAndMint("tacm", "ethereum",
		"tx0src", "0xtgt", 100, "TACM")
	if err != nil {
		t.Fatal(err)
	}
	if res.Fee != 0.1 || res.Received != 99.9 {
		t.Errorf("費用/到賬錯誤: fee=%g received=%g", res.Fee, res.Received)
	}
	id := res.BridgeTxID

	locked, err := b.ConfirmLock(id, "src-lock-hash")
	if err != nil || locked.Status != StatusLocked {
		t.Fatalf("鎖定失敗: %v", err)
	}
	done, err := b.MintOnTarget(id, "tgt-mint-hash")
	if err != nil || done.Status != StatusConfirmed {
		t.Fatalf("鑄造確認失敗: %v", err)
	}
	if done.TargetTxHash != "tgt-mint-hash" || done.ConfirmedAt == 0 {
		t.Error("目標哈希/確認時間缺失")
	}

	// 已 confirmed 不能再 ConfirmLock。
	if _, err := b.ConfirmLock(id, "x"); err == nil {
		t.Error("終態不應再鎖定")
	}
}

func TestBurnUnlockFlow(t *testing.T) {
	b := newTestBridge(t)
	defer b.Close()

	res, err := b.BurnAndUnlock("ethereum", "tacm",
		"0xsrc", "tx0tgt", 50, "TACM")
	if err != nil {
		t.Fatal(err)
	}
	id := res.BridgeTxID
	if _, err := b.ConfirmBurn(id, "burn-hash"); err != nil {
		t.Fatal(err)
	}
	done, err := b.UnlockOnTarget(id, "unlock-hash")
	if err != nil {
		t.Fatalf("解鎖失敗: %v", err)
	}
	if done.Status != StatusConfirmed || done.UnlockTxHash != "unlock-hash" {
		t.Errorf("終態錯誤: %+v", done)
	}
}

func TestUnsupportedChainRejected(t *testing.T) {
	b := newTestBridge(t)
	defer b.Close()
	if _, err := b.LockAndMint("tacm", "solana", "a", "b", 10, "TACM"); err == nil {
		t.Error("不支持鏈應被拒")
	}
	if _, err := b.LockAndMint("tacm", "tacm", "a", "b", 10, "TACM"); err == nil {
		t.Error("同鏈應被拒")
	}
	if _, err := b.LockAndMint("tacm", "ethereum", "a", "b", 0.0001, "TACM"); err == nil {
		t.Error("低於最小額應被拒")
	}
}

func testValidator(t *testing.T, kp *crypto.KeyPair, name string) Validator {
	t.Helper()
	addr, _ := kp.Address()
	return Validator{
		Address: addr, Name: name,
		PublicKey: hex.EncodeToString(kp.PublicKeyCompressed()),
		Active:    true, JoinedAt: time.Now().Unix(),
	}
}

func TestMessageMultiSigQuorum(t *testing.T) {
	v1KP, _ := crypto.GenerateKeyPair()
	v2KP, _ := crypto.GenerateKeyPair()
	v3KP, _ := crypto.GenerateKeyPair()
	vals := []Validator{
		testValidator(t, v1KP, "v1"),
		testValidator(t, v2KP, "v2"),
		testValidator(t, v3KP, "v3"),
	}
	verifier := NewMessageVerifier(2)

	m, err := NewMessage("tacm", "ethereum", MsgLock,
		map[string]any{"amount": "100", "to": "0xtgt"}, 1)
	if err != nil {
		t.Fatal(err)
	}

	// 僅 1 簽名 → 不足門檻。
	s1, _ := m.Sign(v1KP)
	m.AddSignature(s1)
	r1, _ := verifier.Verify(m, vals)
	if r1.OK || r1.SignaturesVerified != 1 {
		t.Fatalf("1 簽名不應過閘: %+v", r1)
	}

	// 補第 2 簽名 → 過閘。
	s2, _ := m.Sign(v2KP)
	m.AddSignature(s2)
	r2, _ := verifier.Verify(m, vals)
	if !r2.OK || r2.SignaturesVerified != 2 {
		t.Fatalf("2 簽名應過閘: %+v", r2)
	}

	// 同一消息（同 nonce）再次驗證 → 重放被拒。
	r3, _ := verifier.Verify(m, vals)
	if r3.OK {
		t.Fatal("重放應被拒")
	}
}

func TestMessageTamperSignatureRejected(t *testing.T) {
	v1KP, _ := crypto.GenerateKeyPair()
	v2KP, _ := crypto.GenerateKeyPair()
	vals := []Validator{
		testValidator(t, v1KP, "v1"),
		testValidator(t, v2KP, "v2"),
	}
	verifier := NewMessageVerifier(2)

	good, _ := NewMessage("tacm", "ethereum", MsgLock,
		map[string]any{"amount": "100"}, 10)
	s1, _ := good.Sign(v1KP)
	s2, _ := good.Sign(v2KP)

	// 攻擊者篡改 payload，得到新 message_id/digest，卻附上原簽名。
	evil, _ := NewMessage("tacm", "ethereum", MsgLock,
		map[string]any{"amount": "999"}, 10)
	evil.AddSignature(s1)
	evil.AddSignature(s2)

	r, _ := verifier.Verify(evil, vals)
	if r.OK {
		t.Fatal("篡改消息的舊簽名不應通過")
	}
}

// TestKillSwitchPaused 驗證 kill-switch：暫停後所有橋操作被拒、恢復後可用、重啟後保持。
func TestKillSwitchPaused(t *testing.T) {
	b := newTestBridge(t)
	defer b.Close()

	if b.IsPaused() {
		t.Fatal("新橋不應預設暫停")
	}
	// 正常操作可用。
	if _, err := b.LockAndMint("tacm", "ethereum", "tx0src", "0xtgt", 100, "TACM"); err != nil {
		t.Fatalf("暫停前鎖定應成功: %v", err)
	}

	if err := b.SetPaused(true); err != nil {
		t.Fatalf("暫停失敗: %v", err)
	}
	if !b.IsPaused() {
		t.Fatal("暫停狀態未生效")
	}
	if _, err := b.LockAndMint("tacm", "ethereum", "tx0src", "0xtgt", 100, "TACM"); err != ErrBridgePaused {
		t.Fatalf("暫停時鎖定應回 ErrBridgePaused，實際 %v", err)
	}
	if _, err := b.BurnAndUnlock("ethereum", "tacm", "0xtgt", "tx0src", 100, "TACM"); err != ErrBridgePaused {
		t.Fatalf("暫停時銷毀應回 ErrBridgePaused，實際 %v", err)
	}

	if err := b.SetPaused(false); err != nil {
		t.Fatalf("恢復失敗: %v", err)
	}
	if _, err := b.LockAndMint("tacm", "ethereum", "tx0src", "0xtgt", 100, "TACM"); err != nil {
		t.Fatalf("恢復後鎖定應成功: %v", err)
	}
}

// TestKillSwitchPersists 驗證暫停狀態持久化：重開 Store 後仍為暫停。
func TestKillSwitchPersists(t *testing.T) {
	dir := t.TempDir()
	b, err := New(DefaultConfig(dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	_ = b.Close()

	b2, err := New(DefaultConfig(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	if !b2.IsPaused() {
		t.Fatal("重啟後暫停狀態應保持")
	}
	if _, err := b2.LockAndMint("tacm", "ethereum", "tx0src", "0xtgt", 100, "TACM"); err != ErrBridgePaused {
		t.Fatalf("重啟後操作仍應被拒，實際 %v", err)
	}
}

// mockSettler M150e：記錄鎖定/釋放呼叫的測試資產結算器。
type mockSettler struct {
	locks    []string
	releases []string
	failLock bool
	failRel  bool
}

func (m *mockSettler) LockOnSource(src string, amt float64) error {
	if m.failLock {
		return errors.New("mock lock fail")
	}
	m.locks = append(m.locks, fmt.Sprintf("%s:%.6f", src, amt))
	return nil
}

func (m *mockSettler) ReleaseOnTarget(tgt string, amt float64) error {
	if m.failRel {
		return errors.New("mock release fail")
	}
	m.releases = append(m.releases, fmt.Sprintf("%s:%.6f", tgt, amt))
	return nil
}

// TestAssetSettlerGoldVault M150e：金庫模型——TAC→BSC 鎖定扣用戶資產入金庫；
// BSC→TAC 解鎖從金庫釋放 received 給目標；任一失敗則不建單/不 confirmed。
func TestAssetSettlerGoldVault(t *testing.T) {
	b := newTestBridge(t)
	defer b.Close()

	ms := &mockSettler{}
	b.SetAssetSettler(ms)

	// 1. TAC→BSC：LockAndMint 應觸發 LockOnSource（金額＝鎖定量，含手續費前）。
	res, err := b.LockAndMint("tacm", "ethereum", "tx0src", "0xtgt", 100, "TACM")
	if err != nil {
		t.Fatal(err)
	}
	if len(ms.locks) != 1 || ms.locks[0] != "tx0src:100.000000" {
		t.Fatalf("鎖定呼叫錯誤: %v", ms.locks)
	}
	id := res.BridgeTxID

	// 2. 鎖定失敗 → 不建單。
	ms.failLock = true
	if _, err := b.LockAndMint("tacm", "ethereum", "tx0src", "0xtgt", 100, "TACM"); err == nil {
		t.Fatal("鎖定失敗仍應返回錯誤")
	}
	if _, err := b.Status(id); err != nil {
		t.Fatal(err)
	}
	ms.failLock = false

	// 3. BSC→TAC：UnlockOnTarget 應觸發 ReleaseOnTarget（金額＝received，扣手續費後）。
	res2, err := b.BurnAndUnlock("ethereum", "tacm", "0xsrc", "tx0tgt", 50, "TACM")
	if err != nil {
		t.Fatal(err)
	}
	id2 := res2.BridgeTxID
	if _, err := b.ConfirmBurn(id2, "burn-hash"); err != nil {
		t.Fatal(err)
	}
	done, err := b.UnlockOnTarget(id2, "unlock-hash")
	if err != nil {
		t.Fatalf("解鎖失敗: %v", err)
	}
	if len(ms.releases) != 1 || ms.releases[0] != "tx0tgt:49.950000" {
		t.Fatalf("釋放呼叫錯誤: %v（期望 tx0tgt:49.950000）", ms.releases)
	}
	if done.Status != StatusConfirmed {
		t.Fatalf("終態=%s 期望 confirmed", done.Status)
	}

	// 4. 釋放失敗 → 不 confirmed（單停留 burning）。
	ms.failRel = true
	res3, err := b.BurnAndUnlock("ethereum", "tacm", "0xsrc", "tx0tgt2", 10, "TACM")
	if err != nil {
		t.Fatal(err)
	}
	id3 := res3.BridgeTxID
	if _, err := b.ConfirmBurn(id3, "burn-hash2"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.UnlockOnTarget(id3, "unlock-hash2"); err == nil {
		t.Fatal("釋放失敗仍應返回錯誤")
	}
	st, err := b.Status(id3)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != StatusBurning {
		t.Fatalf("釋放失敗後狀態=%s 期望 burning", st.Status)
	}
	ms.failRel = false

	// 5. 重試釋放成功 → confirmed（燃燒單可重試）。
	if _, err := b.UnlockOnTarget(id3, "unlock-hash2"); err != nil {
		t.Fatalf("重試解鎖失敗: %v", err)
	}
	st2, err := b.Status(id3)
	if err != nil {
		t.Fatal(err)
	}
	if st2.Status != StatusConfirmed {
		t.Fatalf("重試後狀態=%s 期望 confirmed", st2.Status)
	}
}
