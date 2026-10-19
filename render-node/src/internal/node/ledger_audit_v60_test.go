package node

// v60：審計增量重放（checkpoint 基線）測試：
//  1. 增量結果與全量重放完全一致；
//  2. 基線持久化正確；
//  3. 增量模式仍能抓出基線之後出現的非法交易（且不推進基線）。

import (
	"os"
	"path/filepath"
	"testing"

	"tacm/internal/chaindb"
	"tacm/internal/crypto"
)

// v60GoodChain 造一條 3 塊好鏈（創世＋coinbase＋簽名轉帳＋合約），回傳節點與 alice 地址。
func v60GoodChain(t *testing.T) (*Node, *crypto.KeyPair, string) {
	t.Helper()
	aliceKP, _ := crypto.GenerateKeyPair()
	alice := m71Addr(t, aliceKP)
	bobKP, _ := crypto.GenerateKeyPair()
	bob := m71Addr(t, bobKP)
	n := m71Node(t, alice, "1000")
	m71InsertBlock(t, n, 1, n.Address(), m71CoinbaseTxs(1, alice, n.Address()))
	m71InsertBlock(t, n, 2, n.Address(), []chaindb.Transaction{
		m71SignedTx(t, aliceKP, bob, "10", "0.1", 0, ""),
	})
	m71InsertBlock(t, n, 3, n.Address(), []chaindb.Transaction{
		m71SignedTx(t, aliceKP, bob, "1", "0.05", 1, "vm:call:10000:6000"),
	})
	return n, aliceKP, bob
}

// TestAuditIncrementalMatchesFull 增量重放結果必須與全量重放完全一致。
func TestAuditIncrementalMatchesFull(t *testing.T) {
	n, aliceKP, bob := v60GoodChain(t)

	full1, err := n.VerifyLedgerOnChain()
	if err != nil {
		t.Fatal(err)
	}
	if full1.Incremental {
		t.Fatal("首次（無基線）應為全量重放")
	}
	if !full1.OK {
		t.Fatalf("好鏈應全過: %+v", full1)
	}

	// 基線已建立；再出 2 塊（coinbase＋轉帳）。
	m71InsertBlock(t, n, 4, n.Address(), m71CoinbaseTxs(4, m71Addr(t, aliceKP), n.Address()))
	m71InsertBlock(t, n, 5, n.Address(), []chaindb.Transaction{
		m71SignedTx(t, aliceKP, bob, "3", "0.05", 2, ""),
	})

	inc, err := n.VerifyLedgerOnChain()
	if err != nil {
		t.Fatal(err)
	}
	if !inc.Incremental {
		t.Fatal("基線存在且新塊在後，應走增量重放")
	}
	if inc.FromHeight != 3 {
		t.Fatalf("增量起點應為 3，實際 %d", inc.FromHeight)
	}

	// 刪基線強制全量，逐一比對計數與總額。
	if err := os.Remove(n.auditCheckpointPath()); err != nil {
		t.Fatal(err)
	}
	full2, err := n.VerifyLedgerOnChain()
	if err != nil {
		t.Fatal(err)
	}
	if full2.Incremental {
		t.Fatal("刪除基線後應全量重放")
	}
	if inc.TipHeight != full2.TipHeight || inc.TxChecked != full2.TxChecked ||
		inc.TxSigPass != full2.TxSigPass || inc.TxNoncePass != full2.TxNoncePass ||
		inc.TxBalancePass != full2.TxBalancePass || inc.ContractPass != full2.ContractPass ||
		inc.CoinbaseTx != full2.CoinbaseTx {
		t.Fatalf("增量與全量計數不符:\ninc=%+v\nfull=%+v", inc, full2)
	}
	if inc.CoinbaseTotal != full2.CoinbaseTotal ||
		inc.BalancesTotal != full2.BalancesTotal ||
		inc.BurnedTotal != full2.BurnedTotal {
		t.Fatalf("增量與全量總額不符:\ninc=%+v\nfull=%+v", inc, full2)
	}
	if inc.OK != full2.OK || inc.LedgerOK != full2.LedgerOK {
		t.Fatalf("增量與全量 OK 判定不符:\ninc=%+v\nfull=%+v", inc, full2)
	}
}

// TestAuditCheckpointPersisted 審計成功後基線檔落地且高度正確。
func TestAuditCheckpointPersisted(t *testing.T) {
	n, _, _ := v60GoodChain(t)
	if _, err := n.VerifyLedgerOnChain(); err != nil {
		t.Fatal(err)
	}
	path := n.auditCheckpointPath()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("基線檔應存在: %v", err)
	}
	cp := loadAuditCheckpoint(path)
	if cp == nil {
		t.Fatal("基線應可讀回")
	}
	if cp.Height != 3 {
		t.Fatalf("基線高度應為 3，實際 %d", cp.Height)
	}
	if cp.TxChecked != 2 || cp.ContractPass != 1 {
		t.Fatalf("基線計數不符: %+v", cp)
	}
}

// TestAuditIncrementalDetectsIssue 基線後的非法交易必須被增量重放抓出，且基線不推進。
func TestAuditIncrementalDetectsIssue(t *testing.T) {
	n, aliceKP, bob := v60GoodChain(t)
	if _, err := n.VerifyLedgerOnChain(); err != nil {
		t.Fatal(err)
	}

	// h=4：偽簽名交易（合法結構但簽名無效）。
	bad := m71SignedTx(t, aliceKP, bob, "1", "0.01", 2, "")
	bad.Signature = "0xdeadbeef"
	m71InsertBlock(t, n, 4, n.Address(), []chaindb.Transaction{bad})

	inc, err := n.VerifyLedgerOnChain()
	if err != nil {
		t.Fatal(err)
	}
	if !inc.Incremental {
		t.Fatal("基線存在應走增量")
	}
	if inc.TxSigFail != 1 {
		t.Fatalf("應抓出 1 筆偽簽名: %+v", inc)
	}
	if inc.OK {
		t.Fatal("存在非法交易不應 OK")
	}
	// 基線不推進：仍停在 3（下次仍會重放 h=4 以持續暴露問題）。
	cp := loadAuditCheckpoint(n.auditCheckpointPath())
	if cp == nil || cp.Height != 3 {
		t.Fatalf("基線不應推進: %+v", cp)
	}
}

// TestAuditCheckpointCorruptFallsBackToFull 基線損壞時自動退回全量重放。
func TestAuditCheckpointCorruptFallsBackToFull(t *testing.T) {
	n, _, _ := v60GoodChain(t)
	if _, err := n.VerifyLedgerOnChain(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(n.DataDir(), "audit_checkpoint.json"),
		[]byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := n.VerifyLedgerOnChain()
	if err != nil {
		t.Fatal(err)
	}
	if res.Incremental {
		t.Fatal("基線損壞應全量重放")
	}
	if !res.OK {
		t.Fatalf("好鏈應全過: %+v", res)
	}
}
