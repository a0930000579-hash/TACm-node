package node

// M68 鏈上真實聚合測試：OnChainCoinbaseAggregate 掃鏈 coinbase 交易，
// 總產出／pool 挹注／礦工收益都直接來自鏈上交易，不依賴本地快照。

import (
	"math"
	"testing"
	"time"
)

// TestOnChainCoinbaseAggregate 出塊後聚合：total == 塊數×出塊獎勵；M70 三分發——
// node == 塊數×9%（節點獎勵，給出塊節點）、pool == 塊數×16%、miner/reserve == 75%。
func TestOnChainCoinbaseAggregate(t *testing.T) {
	n, _ := startMiningNode(t)
	waitFor(t, func() bool { return n.DB().GetTipHeight() >= 3 }, 25*time.Second, "等待出塊到 h3")
	agg, err := n.OnChainCoinbaseAggregate()
	if err != nil {
		t.Fatalf("聚合失敗: %v", err)
	}
	tip := n.DB().GetTipHeight()
	if agg.TipHeight != tip {
		t.Fatalf("TipHeight=%d 應等於鏈頂 %d", agg.TipHeight, tip)
	}
	// 每塊 coinbase 交易 3 筆（node＋reserve＋pool；無礦工環境，M70）。
	if agg.Count != int(agg.TipHeight)*3 {
		t.Fatalf("coinbase 筆數=%d 應等於高度 %d×3", agg.Count, agg.TipHeight)
	}
	// 每塊 coinbase 總額 == 出塊獎勵（測試難度 1 初始獎勵 10，M67 冒煙實測值）。
	want := float64(agg.TipHeight) * 10
	if math.Abs(agg.Total-want) > 1e-6 {
		t.Fatalf("Total=%v 應約等於 %v（高度 %d）", agg.Total, want, tip)
	}
	// pool 份額 16%：每塊 10 × 16% = 1.6。
	wantPool := float64(tip) * 1.6
	if math.Abs(agg.Pool-wantPool) > 1e-6 {
		t.Fatalf("Pool=%v 應約等於 %v", agg.Pool, wantPool)
	}
	// node 份額 9%：每塊 10 × 9% = 0.9（M70 節點獎勵）。
	wantNode := float64(tip) * 0.9
	if math.Abs(agg.Node-wantNode) > 1e-6 {
		t.Fatalf("Node=%v 應約等於 %v", agg.Node, wantNode)
	}
	// 無礦工時 miner 收益為空。
	if len(agg.Miners) > 0 {
		t.Fatalf("無礦工環境下 Miners 應為空，實際=%v", agg.Miners)
	}
}

// TestMinerEarnedOnChain 指定地址收益：未挖礦地址鏈上收益為 0（鏈上事實）。
func TestMinerEarnedOnChain(t *testing.T) {
	n, _ := startMiningNode(t)
	waitFor(t, func() bool { return n.DB().GetTipHeight() >= 2 }, 20*time.Second, "等待出塊到 h2")
	v, count, err := n.MinerEarnedOnChain("tx0nobody")
	if err != nil {
		t.Fatalf("查詢失敗: %v", err)
	}
	if v != 0 || count != 0 {
		t.Fatalf("未挖礦地址收益應為 0/0，實際 %v/%d", v, count)
	}
}
