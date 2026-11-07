package node

import (
	"testing"

	"tacm/internal/chaindb"
)

// 長鏈領先：競爭鏈在同難度下塊數更多、總累積難度更重，ReorgChainFull 應一次性接受並切換。
// 這是分段 ReorgChain（單段 CD 對舊鏈全 CD）會誤拒的場景。
func TestReorgChainFullLongerAccepted(t *testing.T) {
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

	// 從 height1 分叉，競爭鏈 h2..h5 共 4 塊（超出本地 tip=3），同難度 1 → 總 CD 4 > 本地 2。
	var blocks []chaindb.Block
	var txs [][]chaindb.Transaction
	prev := b1.Hash
	for h := int64(2); h <= 5; h++ {
		b, bt := mineCompeting(t, n, h, prev, 1)
		blocks = append(blocks, *b)
		txs = append(txs, bt)
		prev = b.Hash
	}
	if err := n.P2PHost().ReorgChainFull(1, blocks, txs); err != nil {
		t.Fatalf("長鏈應被接受: %v", err)
	}
	if tip := n.db.GetTipHeight(); tip != 5 {
		t.Fatalf("重組後高度=%d 應為 5", tip)
	}
	nb, _ := n.db.GetBlock(5)
	if nb.Hash != blocks[3].Hash {
		t.Error("鏈頂未切換到競爭長鏈")
	}
}

// 等重（非更重）競爭鏈必須被拒絕，本地鏈保持不變。
func TestReorgChainFullEqualRejected(t *testing.T) {
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

	var blocks []chaindb.Block
	var txs [][]chaindb.Transaction
	prev := b1.Hash
	for h := int64(2); h <= 3; h++ {
		b, bt := mineCompeting(t, n, h, prev, 1)
		blocks = append(blocks, *b)
		txs = append(txs, bt)
		prev = b.Hash
	}
	if err := n.P2PHost().ReorgChainFull(1, blocks, txs); err == nil {
		t.Fatal("等重競爭鏈應被拒絕，但重組成功")
	}
	tip, _ := n.db.GetBlock(3)
	if tip.Hash != keepTip.Hash {
		t.Error("拒絕重組後本地鏈頂被改變")
	}
}
