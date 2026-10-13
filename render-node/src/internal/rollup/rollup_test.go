package rollup

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"tacm/internal/crypto"
)

func newTestRollup(t *testing.T) *Rollup {
	t.Helper()
	cfg := DefaultConfig(t.TempDir())
	cfg.BatchInterval = 100 * time.Millisecond
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// fakeL1 記錄提交的證明（測試替身）。
type fakeL1 struct {
	mu     sync.Mutex
	proofs []L1Proof
	fail   bool
}

func (f *fakeL1) SubmitStateRoot(p L1Proof) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return "", fmt.Errorf("l1 unavailable")
	}
	f.proofs = append(f.proofs, p)
	return fmt.Sprintf("l1tx%d", len(f.proofs)), nil
}

func l2SignedTx(t *testing.T, kp *crypto.KeyPair, to string,
	amount, fee float64, nonce int64) map[string]any {
	t.Helper()
	addr, _ := kp.Address()
	tx := map[string]any{
		"from": addr, "to": to,
		"amount": strconv.FormatFloat(amount, 'f', -1, 64),
		"fee":    strconv.FormatFloat(fee, 'f', -1, 64),
		"nonce":  nonce, "ts": time.Now().Unix(),
		"pubkey": hex.EncodeToString(kp.PublicKeyCompressed()),
	}
	sig, err := crypto.SignTransaction(tx, kp.PrivateKey())
	if err != nil {
		t.Fatal(err)
	}
	tx["signature"] = sig
	return tx
}

func TestRollupTransferAndBatch(t *testing.T) {
	r := newTestRollup(t)
	defer r.Close()
	aliceKP, _ := crypto.GenerateKeyPair()
	bobKP, _ := crypto.GenerateKeyPair()
	alice, _ := aliceKP.Address()
	bob, _ := bobKP.Address()

	if _, err := r.DepositToL2(alice, 1000, "l1-genesis"); err != nil {
		t.Fatal(err)
	}
	h, err := r.SubmitTransaction(l2SignedTx(t, aliceKP, bob, 100, 1, 0))
	if err != nil {
		t.Fatalf("提交 L2 交易失敗: %v", err)
	}
	if len(r.mempool) != 1 {
		t.Fatalf("交易應在內存池")
	}

	block, err := r.CreateBatch()
	if err != nil || block == nil {
		t.Fatalf("打包失敗: %v", err)
	}
	if block.TxCount != 1 || block.StateRoot == ZeroRoot {
		t.Errorf("塊內容/狀態根錯誤: %+v", block)
	}
	if got := r.tree.Balance(bob); got != 100 {
		t.Errorf("bob 應100, got %g", got)
	}
	if got := r.tree.Balance(alice); got != 899 {
		t.Errorf("alice 應899, got %g", got)
	}
	tt, _ := r.store.GetTx(h)
	if tt == nil || tt.Status != StatusConfirmed || tt.L2Block != 1 {
		t.Errorf("交易狀態錯誤: %+v", tt)
	}
}

func TestRollupSubmitAndChallengeHonest(t *testing.T) {
	r := newTestRollup(t)
	defer r.Close()
	f := &fakeL1{}
	r.SetL1(f)
	aliceKP, _ := crypto.GenerateKeyPair()
	bobKP, _ := crypto.GenerateKeyPair()
	alice, _ := aliceKP.Address()
	bob, _ := bobKP.Address()
	r.DepositToL2(alice, 1000, "g")
	r.SubmitTransaction(l2SignedTx(t, aliceKP, bob, 50, 1, 0))
	r.CreateBatch()

	sub, err := r.SubmitToL1(1)
	if err != nil {
		t.Fatalf("提交 L1 失敗: %v", err)
	}
	if r.blocks[1].Status != BlockSubmitted || len(f.proofs) != 1 {
		t.Fatal("塊應為 submitted")
	}
	// 挑戰期內不能最終化。
	if err := r.FinalizeBlock(1, sub.ChallengeDeadline-1); err == nil {
		t.Error("挑戰期內不應最終化")
	}
	// 誠實塊挑戰應失敗。
	cv, err := r.ChallengeBlock(1, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	if cv.ChallengeSuccess {
		t.Error("誠實塊不應被判欺詐")
	}
	// 挑戰期後最終化。
	if err := r.FinalizeBlock(1, sub.ChallengeDeadline+1); err != nil {
		t.Errorf("最終化失敗: %v", err)
	}
	if r.blocks[1].Status != BlockFinalized {
		t.Error("應為 finalized")
	}
}

func TestRollupChallengeFraud(t *testing.T) {
	r := newTestRollup(t)
	defer r.Close()
	r.SetL1(&fakeL1{})
	aliceKP, _ := crypto.GenerateKeyPair()
	bobKP, _ := crypto.GenerateKeyPair()
	alice, _ := aliceKP.Address()
	bob, _ := bobKP.Address()
	r.DepositToL2(alice, 1000, "g")
	r.SubmitTransaction(l2SignedTx(t, aliceKP, bob, 50, 1, 0))
	r.CreateBatch()
	r.SubmitToL1(1)

	// 篡改提交的狀態根，模擬惡意 proposer 的錯誤承諾。
	r.blocks[1].StateRoot = "0xdeadbeef"
	cv, err := r.ChallengeBlock(1, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	if !cv.ChallengeSuccess {
		t.Fatal("篡改根應被判欺詐")
	}
	if r.blocks[1].Status != BlockChallenged {
		t.Error("塊應被標 challenged")
	}
	// 當前狀態應回滾到執行前（alice 1000、bob 0）。
	if r.tree.Balance(alice) != 1000 || r.tree.Balance(bob) != 0 {
		t.Errorf("回滾狀態錯誤: alice=%g bob=%g",
			r.tree.Balance(alice), r.tree.Balance(bob))
	}
}

func TestRollupDepositReplayRejected(t *testing.T) {
	r := newTestRollup(t)
	defer r.Close()
	aliceKP, _ := crypto.GenerateKeyPair()
	alice, _ := aliceKP.Address()
	if _, err := r.DepositToL2(alice, 100, "h1"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.DepositToL2(alice, 100, "h1"); err == nil {
		t.Error("同一 L1 交易重複入賬應被拒")
	}
	if got := r.tree.Balance(alice); got != 100 {
		t.Errorf("餘額應100, got %g", got)
	}
}

func TestRollupWithdrawalLifecycle(t *testing.T) {
	r := newTestRollup(t)
	defer r.Close()
	aliceKP, _ := crypto.GenerateKeyPair()
	alice, _ := aliceKP.Address()
	r.DepositToL2(alice, 1000, "g")

	wv, err := r.WithdrawToL1(alice, 500)
	if err != nil {
		t.Fatal(err)
	}
	if r.tree.Balance(alice) != 500 {
		t.Error("提款後 L2 應500")
	}
	now := time.Now().Unix()
	if _, err := r.CompleteWithdrawal(wv.WithdrawID, now); err == nil {
		t.Error("挑戰期內不應完成提款")
	}
	w, err := r.CompleteWithdrawal(wv.WithdrawID, wv.ChallengeDeadline+1)
	if err != nil {
		t.Fatalf("完成提款失敗: %v", err)
	}
	if w.Status != "completed" {
		t.Error("提款應 completed")
	}
}

func TestRollupServiceAutoBatch(t *testing.T) {
	r := newTestRollup(t)
	defer r.Close()
	f := &fakeL1{}
	r.SetL1(f)

	svc := StartService(r)
	defer svc.Close()

	aliceKP, _ := crypto.GenerateKeyPair()
	bobKP, _ := crypto.GenerateKeyPair()
	alice, _ := aliceKP.Address()
	bob, _ := bobKP.Address()
	r.DepositToL2(alice, 1000, "g")
	r.SubmitTransaction(l2SignedTx(t, aliceKP, bob, 100, 1, 0))

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		n := len(f.proofs)
		f.mu.Unlock()
		if n >= 1 && r.tree.Balance(bob) == 100 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("後台服務未自動打包並提交 L1")
}
