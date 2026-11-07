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

// 更長的競爭鏈（PoT 時間槽更多）應觸發重組，鏈頂切換且賬戶隨新鏈重建。
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

	// PoT（非 PoW）：鏈權重＝區塊數。從 height1 分叉，構造比本地（分叉後 2 塊）
	// 更長的競爭鏈（3 塊，2'→3'→4'，封印難度固定 1），應被接受。
	b2, t2 := mineCompeting(t, n, 2, b1.Hash, 1)
	b3, t3 := mineCompeting(t, n, 3, b2.Hash, 1)
	b4, t4 := mineCompeting(t, n, 4, b3.Hash, 1)

	oldTip, _ := n.db.GetBlock(3)
	if err := n.P2PHost().ReorgChain(1,
		[]chaindb.Block{*b2, *b3, *b4},
		[][]chaindb.Transaction{t2, t3, t4}); err != nil {
		t.Fatalf("重組失敗: %v", err)
	}

	tip := n.db.GetTipHeight()
	if tip != 4 {
		t.Fatalf("重組後高度=%d 應為 4", tip)
	}
	newTip, _ := n.db.GetBlock(4)
	if newTip.Hash != b4.Hash {
		t.Errorf("鏈頂未切換到競爭鏈: %s", newTip.Hash)
	}
	if newTip.Hash == oldTip.Hash {
		t.Error("重組前後鏈頂相同，分叉未生效")
	}

	// 賬戶應與新鏈一致：本地 h1 節點 45% 獎勵 4.5 進帳；競爭塊 h2'/h3'/h4' 由
	// mineCompeting 手工 coinbase 給提議者（3×10=30）→ 節點餘額 = 34.5。
	bal, _ := parseBal(n.db.GetBalance(n.Address()))
	if bal != 34.5 {
		t.Errorf("重組後節點餘額=%g 應為 34.5", bal)
	}

// 等長（PoT 時間槽未佔優）的競爭鏈必須被拒絕，本地鏈保持不變。
}
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

	// 競爭鏈與本地分叉後等長（各 2 塊，PoT 時間槽未佔優），應被拒絕。
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

// 同高度（等長）分叉以確定性 tie-break 收斂：僅當新鏈 tip 哈希字典序更小時切換。
func TestP2PReorgTieBreak(t *testing.T) {
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
	localTip, _ := n.db.GetBlock(3)

	// 等長競爭鏈（分叉後 2 塊，封印難度固定 1）。
	b2, t2 := mineCompeting(t, n, 2, b1.Hash, 1)
	b3, t3 := mineCompeting(t, n, 3, b2.Hash, 1)

	// 依實際哈希字典序決定預期：更小→接受，否則→拒絕（全網獨立計算同一 winner）。
	shouldAccept := b3.Hash < localTip.Hash
	err = n.P2PHost().ReorgChainTieBreak(1,
		[]chaindb.Block{*b2, *b3},
		[][]chaindb.Transaction{t2, t3})
	if shouldAccept {
		if err != nil {
			t.Fatalf("tip 哈希更小應接受 tie-break，實際拒絕: %v", err)
		}
		tip, _ := n.db.GetBlock(3)
		if tip.Hash != b3.Hash {
			t.Fatal("tie-break 接受後鏈頂未切換到競爭鏈")
		}
	} else {
		if err == nil {
			t.Fatal("tip 哈希不更小應拒絕 tie-break，但重組成功")
		}
		tip, _ := n.db.GetBlock(3)
		if tip.Hash != localTip.Hash {
			t.Error("tie-break 拒絕後本地鏈頂被改變")
		}
	}
}
