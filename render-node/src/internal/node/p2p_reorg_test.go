package node

import (
	"strconv"
	"testing"
	"time"

	"tacm/internal/chaindb"
	"tacm/internal/crypto"
)

// mineCompeting 構造並挖出一條競爭鏈上的區塊（coinbase 首位，可附普通交易）。
func mineCompeting(t *testing.T, n *Node, height int64, prevHash string, diff int) (*chaindb.Block, []chaindb.Transaction) {
	t.Helper()
	ts := time.Now().Unix()
	cb := chaindb.BuildCoinbaseTx(height, n.nodeAddress, ts)
	txs := []chaindb.Transaction{cb}
	mroot, err := crypto.MerkleRootStrings([]string{cb.TxHash})
	if err != nil {
		t.Fatal(err)
	}
	header := map[string]any{
		"height": height, "prev_hash": prevHash, "merkle_root": mroot,
		"proposer": n.nodeID, "ts": ts, "tx_count": len(txs), "difficulty": diff,
	}
	pow, err := crypto.MineBlock(header, diff, 20_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if pow == nil {
		t.Fatalf("競爭塊 h=%d diff=%d 未找到 PoW nonce", height, diff)
	}
	b := &chaindb.Block{
		Height: height, PrevHash: &prevHash, MerkleRoot: mroot,
		Hash: pow.Hash, Proposer: n.nodeID, ProposerAddress: n.nodeAddress,
		Ts: ts, TxCount: len(txs), Difficulty: diff, Nonce: int64(pow.Nonce),
	}
	return b, txs
}

// 更重的競爭鏈應觸發重組，鏈頂切換且賬戶隨新鏈重建。
func TestP2PReorgHeavier(t *testing.T) {
	n, err := New(testCfg(t), "node1", 1)
	if err != nil {
		t.Fatal(err)
	}
	insertGenesis(t, n)
	for i := 0; i < 3; i++ {
		if err := n.produceBlock(); err != nil {
			t.Fatalf("手動出塊 %d 失敗: %v", i+1, err)
		}
	}
	defer func() { _ = n.Close() }()

	b1, err := n.db.GetBlock(1)
	if err != nil {
		t.Fatal(err)
	}

	// 從 height1 之後分叉，競爭塊以難度 2 構造更重鏈（累積 4 > 本地累積 2）。
	b2, t2 := mineCompeting(t, n, 2, b1.Hash, 2)
	b3, t3 := mineCompeting(t, n, 3, b2.Hash, 2)

	oldTip, _ := n.db.GetBlock(3)
	if err := n.P2PHost().ReorgChain(1,
		[]chaindb.Block{*b2, *b3},
		[][]chaindb.Transaction{t2, t3}); err != nil {
		t.Fatalf("重組失敗: %v", err)
	}

	tip := n.db.GetTipHeight()
	if tip != 3 {
		t.Fatalf("重組後高度=%d 應為 3", tip)
	}
	newTip, _ := n.db.GetBlock(3)
	if newTip.Hash != b3.Hash {
		t.Errorf("鏈頂未切換到競爭鏈: %s", newTip.Hash)
	}
	if newTip.Hash == oldTip.Hash {
		t.Error("重組前後鏈頂相同，分叉未生效")
	}

	// 賬戶應與新鏈一致：本地 h1（無開機礦工）節點 9% 獎勵 0.9 進帳（M70 節點獎勵歸節點）；
	// 競爭塊 h2'/h3' 由 mineCompeting 手工 coinbase 給提議者（2×10=20）→ 節點餘額 = 20.9。
	bal, _ := parseBal(n.db.GetBalance(n.Address()))
	if bal != 20.9 {
		t.Errorf("重組後節點餘額=%g 應為 20.9", bal)
	}
}

// 累積難度不更重的競爭鏈必須被拒絕，本地鏈保持不變。
func TestP2PReorgRejectLight(t *testing.T) {
	n, err := New(testCfg(t), "node1", 1)
	if err != nil {
		t.Fatal(err)
	}
	insertGenesis(t, n)
	for i := 0; i < 3; i++ {
		if err := n.produceBlock(); err != nil {
			t.Fatalf("手動出塊 %d 失敗: %v", i+1, err)
		}
	}
	defer func() { _ = n.Close() }()

	b1, _ := n.db.GetBlock(1)
	keepTip, _ := n.db.GetBlock(3)

	// 競爭塊難度 1，累積 2，不重於本地 height2..3（累積 2）。
	b2, t2 := mineCompeting(t, n, 2, b1.Hash, 1)
	b3, t3 := mineCompeting(t, n, 3, b2.Hash, 1)

	err = n.P2PHost().ReorgChain(1,
		[]chaindb.Block{*b2, *b3},
		[][]chaindb.Transaction{t2, t3})
	if err == nil {
		t.Fatal("更輕的競爭鏈應被拒絕，但重組成功")
	}

	tip, _ := n.db.GetBlock(3)
	if tip.Hash != keepTip.Hash {
		t.Error("拒絕重組後本地鏈頂被改變")
	}
}

func parseBal(s string) (float64, error) {
	f, err := strconv.ParseFloat(s, 64)
	return f, err
}
