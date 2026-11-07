package node

// v129：鏈上多簽錢包單元測試——建立/提交/確認/執行/取消完整流程。

import (
	"errors"
	"path/filepath"
	"testing"
)

func newTestMultisig(t *testing.T) *MultisigManager {
	t.Helper()
	dir := t.TempDir()
	m, err := NewMultisigManager(filepath.Join(dir, "multisig.db"))
	if err != nil {
		t.Fatalf("NewMultisigManager: %v", err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

func TestMultisigCreateWallet(t *testing.T) {
	m := newTestMultisig(t)
	// 非法：無 owners
	if err := m.CreateWallet("tx0000000000000000000001", nil, 1, 10, ""); err == nil {
		t.Fatal("expected error for empty owners")
	}
	// 非法：threshold 超出
	if err := m.CreateWallet("tx0000000000000000000001", []string{"tx0000000000000000000002"}, 2, 10, ""); err == nil {
		t.Fatal("expected error for threshold>owners")
	}
	// 正常建立 2-of-3
	owners := []string{"tx0000000000000000000002", "tx0000000000000000000003", "tx0000000000000000000004"}
	if err := m.CreateWallet("tx0000000000000000000001", owners, 2, 10, "hash1"); err != nil {
		t.Fatalf("CreateWallet: %v", err)
	}
	// 重複建立報錯
	if err := m.CreateWallet("tx0000000000000000000001", owners, 2, 11, ""); err == nil {
		t.Fatal("expected error for duplicate wallet")
	}
	w, err := m.WalletInfo("tx0000000000000000000001")
	if err != nil || w == nil {
		t.Fatalf("WalletInfo: %v %v", w, err)
	}
	if w.Threshold != 2 || len(w.Owners) != 3 {
		t.Fatalf("unexpected wallet: %+v", w)
	}
	if ok, _ := m.IsOwner("tx0000000000000000000001", "tx0000000000000000000003"); !ok {
		t.Fatal("owner should be member")
	}
	if ok, _ := m.IsOwner("tx0000000000000000000001", "tx0000000000000000000009"); ok {
		t.Fatal("non-owner should not be member")
	}
}

func TestMultisigSubmitConfirmExecute(t *testing.T) {
	m := newTestMultisig(t)
	wallet := "tx0000000000000000000001"
	o1 := "tx0000000000000000000002"
	o2 := "tx0000000000000000000003"
	if err := m.CreateWallet(wallet, []string{o1, o2}, 2, 10, ""); err != nil {
		t.Fatalf("CreateWallet: %v", err)
	}
	// 非 owner 提交被拒
	if _, err := m.SubmitTx(wallet, o1, "TACm", "1000000000000000000", "", "tx0000000000000000000009", 20); err == nil {
		t.Fatal("expected error for non-owner submit")
	}
	id, err := m.SubmitTx(wallet, o1, "TACm", "1000000000000000000000", "rent", o1, 20)
	if err != nil {
		t.Fatalf("SubmitTx: %v", err)
	}
	// 未達門檻不可執行
	_, err = m.ExecuteTx(id, o1, func(a, b, c, d, e string) (string, error) { return "exec", nil })
	if err == nil {
		t.Fatal("expected error executing below threshold")
	}
	// 重複確認仍為 1
	n, err := m.ConfirmTx(id, o1, 21)
	if err != nil {
		t.Fatalf("ConfirmTx: %v", err)
	}
	if n != 1 {
		t.Fatalf("confirmations want 1 got %d", n)
	}
	if _, err := m.ConfirmTx(id, o1, 22); err != nil {
		t.Fatalf("ConfirmTx dup: %v", err)
	}
	n, _ = m.ConfirmTx(id, o2, 23)
	if n != 2 {
		t.Fatalf("confirmations want 2 got %d", n)
	}
	// 執行（fake execFn）
	executed := false
	tx, err := m.ExecuteTx(id, o1, func(a, b, c, d, e string) (string, error) {
		executed = true
		return "0xexec", nil
	})
	if err != nil {
		t.Fatalf("ExecuteTx: %v", err)
	}
	if !executed || tx.Status != msStatusExecuted || tx.ExecTxHash != "0xexec" {
		t.Fatalf("unexpected executed tx: %+v", tx)
	}
	// 已執行不可再執行
	if _, err := m.ExecuteTx(id, o2, func(a, b, c, d, e string) (string, error) { return "", nil }); err == nil {
		t.Fatal("expected error executing executed tx")
	}
	// 執行錯誤回傳
	id2, _ := m.SubmitTx(wallet, o2, "TACm", "1", "", o2, 30)
	_, _ = m.ConfirmTx(id2, o1, 31)
	_, _ = m.ConfirmTx(id2, o2, 32)
	_, err = m.ExecuteTx(id2, o1, func(a, b, c, d, e string) (string, error) {
		return "", errors.New("insufficient balance")
	})
	if err == nil {
		t.Fatal("expected error propagating execFn failure")
	}
}

func TestMultisigCancel(t *testing.T) {
	m := newTestMultisig(t)
	wallet := "tx0000000000000000000001"
	o1 := "tx0000000000000000000002"
	o2 := "tx0000000000000000000003"
	if err := m.CreateWallet(wallet, []string{o1, o2}, 2, 10, ""); err != nil {
		t.Fatalf("CreateWallet: %v", err)
	}
	id, _ := m.SubmitTx(wallet, o2, "TACm", "1", "", o1, 20)
	// 非提交者不可取消
	if err := m.CancelTx(id, o2); err == nil {
		t.Fatal("expected error cancelling as non-submitter")
	}
	if err := m.CancelTx(id, o1); err != nil {
		t.Fatalf("CancelTx: %v", err)
	}
	// 已取消不可再取消
	if err := m.CancelTx(id, o1); err == nil {
		t.Fatal("expected error cancelling cancelled tx")
	}
}

func TestMultisigWalletsAndTxs(t *testing.T) {
	m := newTestMultisig(t)
	wallet := "tx0000000000000000000001"
	o1 := "tx0000000000000000000002"
	if err := m.CreateWallet(wallet, []string{o1}, 1, 10, ""); err != nil {
		t.Fatalf("CreateWallet: %v", err)
	}
	if _, err := m.SubmitTx(wallet, o1, "TACm", "5", "memo", o1, 20); err != nil {
		t.Fatalf("SubmitTx: %v", err)
	}
	wallets, err := m.Wallets()
	if err != nil || len(wallets) != 1 {
		t.Fatalf("Wallets: %v %v", wallets, err)
	}
	txs, err := m.Txs(wallet)
	if err != nil || len(txs) != 1 {
		t.Fatalf("Txs: %v %v", txs, err)
	}
	if txs[0].Memo != "memo" || txs[0].Status != msStatusPending {
		t.Fatalf("unexpected tx: %+v", txs[0])
	}
	cons, err := m.TxConfirmations(txs[0].ID)
	if err != nil || len(cons) != 0 {
		t.Fatalf("TxConfirmations: %v %v", cons, err)
	}
}

func TestMultisigOwnerDedup(t *testing.T) {
	m := newTestMultisig(t)
	// 重複 owner 去重＋threshold 以去重後長度為界
	if err := m.CreateWallet("tx0000000000000000000001", []string{"tx0000000000000000000002", "tx0000000000000000000002"}, 1, 1, ""); err != nil {
		t.Fatalf("CreateWallet: %v", err)
	}
	w, _ := m.WalletInfo("tx0000000000000000000001")
	if len(w.Owners) != 1 {
		t.Fatalf("owners should be deduped: %+v", w.Owners)
	}
}
