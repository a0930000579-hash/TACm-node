package node

// M116：主網壓力測試——端到端區塊吞吐基準（真實簽名＋創世基金餘額＋完整出塊）。
// 測量：簽名/驗證、入池、打包、執行、寫入的整體 TPS 與單塊延遲。

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"tacm/internal/chaindb"
	"tacm/internal/config"
	"tacm/internal/crypto"
	"tacm/internal/wallet"
)

// benchNodeB 建立具創世基金（100000 TACm）的基準節點（benchmark 用）。
func benchNodeB(b *testing.B, blockTime int) *Node {
	b.Helper()
	cfg := config.Default()
	cfg.DataDir = b.TempDir()
	cfg.BlockTime = blockTime
	n, err := New(cfg, "node1", 1)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	seedGenesis(b, n)
	b.Cleanup(func() { _ = n.Close() })
	return n
}

// benchNodeT 建立具創世基金的基準節點（冒煙測試用）。
func benchNodeT(t *testing.T, blockTime int) *Node {
	t.Helper()
	cfg := testCfg(t)
	cfg.BlockTime = blockTime
	n, err := New(cfg, "node1", 1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	seedGenesis(t, n)
	t.Cleanup(func() { _ = n.Close() })
	return n
}

// seedGenesis 寫入創世塊（社群基金 100000 TACm）。
func seedGenesis(b testing.TB, n *Node) {
	b.Helper()
	pool, err := n.CommunityPoolAddress()
	if err != nil {
		b.Fatalf("CommunityPoolAddress: %v", err)
	}
	now := time.Now().Unix()
	genTx := chaindb.Transaction{
		TxHash: "gen-community-pool", ToAddr: pool,
		Amount: "100000", Fee: "0", Ts: now, Memo: "genesis:community_pool",
	}
	g := &chaindb.Block{Height: 0, Hash: strings.Repeat("0", 64), Proposer: "genesis", Ts: now, TxCount: 1}
	if err := n.DB().InsertBlock(g, []chaindb.Transaction{genTx}); err != nil {
		b.Fatalf("創世失敗: %v", err)
	}
	// 創世基金同步進錢包帳本（wallet 視角）——否則普通轉帳餘額校驗視為 0。
	if n.walletSvc != nil {
		amt, aerr := wallet.NewAmount("100000", wallet.AssetTACm)
		if aerr != nil {
			b.Fatalf("金額: %v", aerr)
		}
		if derr := n.walletSvc.Deposit(pool, wallet.AssetTACm, amt, "genesis:community_pool"); derr != nil {
			b.Fatalf("wallet 注資: %v", derr)
		}
	}
}

// signBenchTxs 用 pool 私鑰簽名 n 筆轉帳（不同 nonce），回傳 map 格式交易。
func signBenchTxs(b testing.TB, n *Node, count int) []map[string]any {
	b.Helper()
	kp, err := n.communityPoolKeypair()
	if err != nil {
		b.Fatalf("pool keypair: %v", err)
	}
	from, _ := kp.Address()
	to, _ := n.keypair.Address()
	// nonce 基準＝已確認 nonce＋pending 佔用（與 SubmitTransaction 校驗一致）。
	nonce := n.db.GetNonce(from) + n.pendingTxCount(from)
	now := time.Now().Unix()
	txs := make([]map[string]any, 0, count)
	for i := 0; i < count; i++ {
		tx := map[string]any{
			"from": from, "to": to,
			"amount": "1", "fee": "0.02",
			"nonce": nonce + int64(i), "ts": now + int64(i),
			"pubkey": hex.EncodeToString(kp.PublicKeyCompressed()),
		}
		sig, err := crypto.SignTransaction(tx, kp.PrivateKey())
		if err != nil {
			b.Fatalf("sign: %v", err)
		}
		tx["signature"] = sig
		txs = append(txs, tx)
	}
	return txs
}

// BenchmarkSubmitTx 交易提交吞吐：簽名＋驗證＋入池（不含出塊）。
func BenchmarkSubmitTx(b *testing.B) {
	n := benchNodeB(b, 1)
	kp, err := n.communityPoolKeypair()
	if err != nil {
		b.Fatalf("pool keypair: %v", err)
	}
	from, _ := kp.Address()
	to, _ := n.keypair.Address()
	pub := hex.EncodeToString(kp.PublicKeyCompressed())
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		nonce := n.db.GetNonce(from) + n.pendingTxCount(from)
		tx := map[string]any{
			"from": from, "to": to,
			"amount": "1", "fee": "0.02",
			"nonce": nonce, "ts": time.Now().Unix(),
			"pubkey": pub,
		}
		sig, serr := crypto.SignTransaction(tx, kp.PrivateKey())
		if serr != nil {
			b.Fatalf("sign: %v", serr)
		}
		tx["signature"] = sig
		if _, serr := n.SubmitTransaction(tx); serr != nil {
			b.Fatalf("submit: %v", serr)
		}
	}
}

// BenchmarkMintBlock 區塊打包吞吐：mempool 500 筆 → 完整出塊（打包+執行+寫入）。
func BenchmarkMintBlock(b *testing.B) {
	n := benchNodeB(b, 1)
	txs := signBenchTxs(b, n, 500)
	for i := 0; i < 500; i++ {
		if _, err := n.SubmitTransaction(txs[i]); err != nil {
			b.Fatalf("submit: %v", err)
		}
	}
	b.ResetTimer()
	start := time.Now()
	if err := n.produceBlock(); err != nil {
		b.Fatalf("produce: %v", err)
	}
	elapsed := time.Since(start)
	b.ReportMetric(500.0/elapsed.Seconds(), "tx/s")
	b.ReportMetric(elapsed.Seconds()*1000, "ms/block")
}

// TestM116Smoke 冒煙：500 筆真實簽名交易完整出塊後鏈上餘額正確（性能回歸門）。
func TestM116Smoke(t *testing.T) {
	n := benchNodeT(t, 1)
	pool, _ := n.CommunityPoolAddress()
	before := n.db.GetBalance(pool)
	txs := signBenchTxs(t, n, 500)
	for i := 0; i < 500; i++ {
		if _, err := n.SubmitTransaction(txs[i]); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}
	if err := n.produceBlock(); err != nil {
		t.Fatalf("produce: %v", err)
	}
	blk, err := n.db.GetLatestBlock()
	if err != nil || blk == nil {
		t.Fatalf("latest block: %v", err)
	}
	if blk.TxCount < 500 {
		t.Fatalf("區塊應含 500 筆交易，得 %d", blk.TxCount)
	}
	after := n.db.GetBalance(pool)
	// 500 筆 × (1 + 0.02) = 510；coinbase 增發歸 proposer（非 pool）。
	spent := parseTACM(before) - parseTACM(after)
	if spent < 500 || spent > 520 {
		t.Fatalf("pool 支出異常: before %s after %s", before, after)
	}
	t.Logf("M116 冒煙通過：區塊 %d 筆交易，pool 支出 %v TACm", blk.TxCount, spent)
}
