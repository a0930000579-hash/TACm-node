package node

// M71 交易與合約層鏈上審計測試：
// 好鏈（創世分配＋coinbase＋簽名轉帳＋合約）全過；
// 壞鏈（偽簽名／nonce 跳號／透支／非法合約 memo）逐一被抓出。

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"tacm/internal/chaindb"
	"tacm/internal/crypto"
)

// m71Node 建立帶創世分配的節點（alice 獲 allocAmt）。
func m71Node(t *testing.T, aliceAddr, allocAmt string) *Node {
	t.Helper()
	n, err := New(testCfg(t), "node1", 1)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	genTx := chaindb.Transaction{
		TxHash: "genalloc", ToAddr: aliceAddr, Amount: allocAmt, Fee: "0", Ts: now,
	}
	g := &chaindb.Block{
		Height: 0, Hash: strings.Repeat("0", 64),
		Proposer: "genesis", Ts: now, TxCount: 1,
	}
	if err := n.DB().InsertBlock(g, []chaindb.Transaction{genTx}); err != nil {
		t.Fatal(err)
	}
	return n
}

// m71InsertBlock 插入一個區塊（交易原樣上鏈，不經 SubmitTransaction——審計正是抓這類「已上鏈但可能非法」的交易）。
func m71InsertBlock(t *testing.T, n *Node, h int64, proposer string, txs []chaindb.Transaction) {
	t.Helper()
	b := &chaindb.Block{
		Height: h, Hash: fmt.Sprintf("%064x", h),
		Proposer: "node1", ProposerAddress: proposer,
		Ts: time.Now().Unix(), TxCount: len(txs), Difficulty: 1,
	}
	if err := n.DB().InsertBlock(b, txs); err != nil {
		t.Fatal(err)
	}
}

// m71CoinbaseTxs 構造一個標準出塊的 coinbase 三筆（node/pool/miner，總額==BlockReward）。
func m71CoinbaseTxs(h int64, minerAddr, nodeAddr string) []chaindb.Transaction {
	now := time.Now().Unix()
	reward := chaindb.BlockReward(h)
	poolAmt := chaindb.FormatFloat(reward * float64(chaindb.PoolShareBps) / 10000)
	nodeAmt := chaindb.FormatFloat(reward * float64(chaindb.NodeShareBps) / 10000)
	minerAmt := chaindb.FormatFloat(reward - reward*float64(chaindb.PoolShareBps)/10000 -
		reward*float64(chaindb.NodeShareBps)/10000)
	return []chaindb.Transaction{
		{TxHash: fmt.Sprintf("cb-node-%d", h), ToAddr: nodeAddr, Amount: nodeAmt, Fee: "0", Memo: "coinbase:node", Ts: now},
		{TxHash: fmt.Sprintf("cb-pool-%d", h), ToAddr: "reward_pool", Amount: poolAmt, Fee: "0", Memo: "coinbase:pool", Ts: now},
		{TxHash: fmt.Sprintf("cb-miner-%d", h), ToAddr: minerAddr, Amount: minerAmt, Fee: "0", Memo: "coinbase:miner", Ts: now},
	}
}

func m71Addr(t *testing.T, kp *crypto.KeyPair) string {
	t.Helper()
	a, err := crypto.PubKeyToAddress(kp.PublicKeyCompressed())
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// m71SignedTx 構造簽名交易（memo 在簽名前設置，與生產 submit 一致）。
func m71SignedTx(t *testing.T, kp *crypto.KeyPair, to, amount, fee string, nonce int64, memo string) chaindb.Transaction {
	t.Helper()
	tx := map[string]any{
		"from": m71Addr(t, kp), "to": to, "amount": amount, "fee": fee,
		"nonce": nonce, "ts": time.Now().Unix(),
		"pubkey": hex.EncodeToString(kp.PublicKeyCompressed()),
		"memo":   memo,
	}
	sig, err := crypto.SignTransaction(tx, kp.PrivateKey())
	if err != nil {
		t.Fatal(err)
	}
	return chaindb.Transaction{
		TxHash:   "tx-" + tx["from"].(string)[:12] + "-" + fmt.Sprint(nonce),
		FromAddr: tx["from"].(string), ToAddr: to, Amount: amount, Fee: fee,
		Nonce: nonce, Ts: tx["ts"].(int64), Signature: sig,
		Pubkey: tx["pubkey"].(string), Memo: memo,
	}
}

// TestLedgerAudit_GoodChain 好鏈：創世分配＋coinbase＋簽名轉帳＋合法合約——全過。
func TestLedgerAudit_GoodChain(t *testing.T) {
	aliceKP, _ := crypto.GenerateKeyPair()
	alice := m71Addr(t, aliceKP)
	bobKP, _ := crypto.GenerateKeyPair()
	bob := m71Addr(t, bobKP)
	n := m71Node(t, alice, "1000")

	// h=1：出塊（coinbase 全給 alice 礦工＋node）。
	m71InsertBlock(t, n, 1, n.Address(), m71CoinbaseTxs(1, alice, n.Address()))
	// h=2：alice 簽名轉帳 10 給 bob（fee 0.1，nonce=0）——餘額 1000+7.5=1007.5 充足。
	m71InsertBlock(t, n, 2, n.Address(), []chaindb.Transaction{
		m71SignedTx(t, aliceKP, bob, "10", "0.1", 0, ""),
	})
	// h=3：alice 合法合約調用（vm:call 帶 gas＋hex payload）。
	m71InsertBlock(t, n, 3, n.Address(), []chaindb.Transaction{
		m71SignedTx(t, aliceKP, bob, "1", "0.05", 1, "vm:call:10000:6000"),
	})

	la, err := n.VerifyLedgerOnChain()
	if err != nil {
		t.Fatal(err)
	}
	if !la.OK {
		t.Fatalf("好鏈應全過: %+v", la)
	}
	if !la.LedgerOK {
		t.Fatalf("守恆應成立: coinbase=%v balances=%v", la.CoinbaseTotal, la.BalancesTotal)
	}
	if la.TxChecked != 2 || la.TxSigPass != 2 || la.TxNoncePass != 2 || la.TxBalancePass != 2 {
		t.Fatalf("交易統計不符: %+v", la)
	}
	if la.ContractPass != 1 || la.ContractFail != 0 {
		t.Fatalf("合約統計不符: %+v", la)
	}
	if la.TxSigFail+la.TxNonceFail+la.TxBalanceFail != 0 {
		t.Fatalf("好鏈不應有失敗: %+v", la)
	}
	if len(la.Issues) != 0 {
		t.Fatalf("好鏈不應有 issues: %+v", la.Issues)
	}
}

// TestLedgerAudit_BadSignature 偽簽名交易上鏈——簽名層被抓出。
func TestLedgerAudit_BadSignature(t *testing.T) {
	aliceKP, _ := crypto.GenerateKeyPair()
	alice := m71Addr(t, aliceKP)
	bobKP, _ := crypto.GenerateKeyPair()
	bob := m71Addr(t, bobKP)
	n := m71Node(t, alice, "100")

	m71InsertBlock(t, n, 1, n.Address(), m71CoinbaseTxs(1, alice, n.Address()))
	// 用 bob 的私鑰簽 alice 的交易（from 偽裝成 alice）→ 簽名必然失敗。
	tx := map[string]any{
		"from": alice, "to": bob, "amount": "5", "fee": "0.1",
		"nonce": 0, "ts": time.Now().Unix(),
		"pubkey": hex.EncodeToString(bobKP.PublicKeyCompressed()), "memo": "",
	}
	sig, err := crypto.SignTransaction(tx, bobKP.PrivateKey())
	if err != nil {
		t.Fatal(err)
	}
	m71InsertBlock(t, n, 2, n.Address(), []chaindb.Transaction{
		{TxHash: "tx-badsig", FromAddr: alice, ToAddr: bob, Amount: "5", Fee: "0.1",
			Nonce: 0, Ts: tx["ts"].(int64), Signature: sig, Pubkey: tx["pubkey"].(string), Memo: ""},
	})

	la, _ := n.VerifyLedgerOnChain()
	if la.TxSigFail != 1 {
		t.Fatalf("偽簽名應被抓出: %+v", la)
	}
	if la.OK {
		t.Fatal("含偽簽名鏈不應 OK")
	}
}

// TestLedgerAudit_BadNonce nonce 跳號——nonce 層被抓出。
func TestLedgerAudit_BadNonce(t *testing.T) {
	aliceKP, _ := crypto.GenerateKeyPair()
	alice := m71Addr(t, aliceKP)
	bobKP, _ := crypto.GenerateKeyPair()
	bob := m71Addr(t, bobKP)
	n := m71Node(t, alice, "100")

	m71InsertBlock(t, n, 1, n.Address(), m71CoinbaseTxs(1, alice, n.Address()))
	// nonce=5（應為 0），但簽名包含 nonce=5 因此簽名本身有效——nonce 檢查必須獨立抓出。
	m71InsertBlock(t, n, 2, n.Address(), []chaindb.Transaction{
		m71SignedTx(t, aliceKP, bob, "5", "0.1", 5, ""),
	})

	la, _ := n.VerifyLedgerOnChain()
	if la.TxNonceFail != 1 {
		t.Fatalf("nonce 跳號應被抓出: %+v", la)
	}
	if la.TxSigFail != 0 {
		t.Fatalf("簽名本身有效，不應誤報簽名: %+v", la)
	}
	if la.OK {
		t.Fatal("含 nonce 異常鏈不應 OK")
	}
}

// TestLedgerAudit_Overdraft 透支交易——餘額層被抓出。
func TestLedgerAudit_Overdraft(t *testing.T) {
	aliceKP, _ := crypto.GenerateKeyPair()
	alice := m71Addr(t, aliceKP)
	bobKP, _ := crypto.GenerateKeyPair()
	bob := m71Addr(t, bobKP)
	n := m71Node(t, alice, "10") // 創世僅 10，無 coinbase

	// 轉帳 9999（遠超餘額）——簽名有效但透支。
	m71InsertBlock(t, n, 1, n.Address(), []chaindb.Transaction{
		m71SignedTx(t, aliceKP, bob, "9999", "1", 0, ""),
	})

	la, _ := n.VerifyLedgerOnChain()
	if la.TxBalanceFail != 1 {
		t.Fatalf("透支應被抓出: %+v", la)
	}
	if la.TxSigFail != 0 || la.TxNonceFail != 0 {
		t.Fatalf("不應誤報簽名/nonce: %+v", la)
	}
	if la.OK {
		t.Fatal("含透支鏈不應 OK")
	}
}

// TestLedgerAudit_BadContractMemo 非法合約 memo（payload 非 hex）——合約層被抓出。
func TestLedgerAudit_BadContractMemo(t *testing.T) {
	aliceKP, _ := crypto.GenerateKeyPair()
	alice := m71Addr(t, aliceKP)
	bobKP, _ := crypto.GenerateKeyPair()
	bob := m71Addr(t, bobKP)
	n := m71Node(t, alice, "100")

	m71InsertBlock(t, n, 1, n.Address(), m71CoinbaseTxs(1, alice, n.Address()))
	// memo="vm:call:10000:zzzz"——gas 可解析但 payload 非 hex；簽名包含該 memo 所以簽名有效。
	m71InsertBlock(t, n, 2, n.Address(), []chaindb.Transaction{
		m71SignedTx(t, aliceKP, bob, "1", "0.1", 0, "vm:call:10000:zzzz"),
	})

	la, _ := n.VerifyLedgerOnChain()
	if la.ContractFail != 1 {
		t.Fatalf("非法合約 memo 應被抓出: %+v", la)
	}
	if la.TxSigFail != 0 {
		t.Fatalf("簽名含 memo 本身有效，不應誤報: %+v", la)
	}
	if la.OK {
		t.Fatal("含非法合約鏈不應 OK")
	}
}

// TestLedgerAudit_EmptyChain 僅創世（空鏈）——通過且守恆。
func TestLedgerAudit_EmptyChain(t *testing.T) {
	aliceKP, _ := crypto.GenerateKeyPair()
	n := m71Node(t, m71Addr(t, aliceKP), "0")
	la, err := n.VerifyLedgerOnChain()
	if err != nil {
		t.Fatal(err)
	}
	if !la.OK || !la.LedgerOK {
		t.Fatalf("空鏈應通過: %+v", la)
	}
	if la.TxChecked != 0 {
		t.Fatalf("空鏈不應有交易: %+v", la)
	}
}
