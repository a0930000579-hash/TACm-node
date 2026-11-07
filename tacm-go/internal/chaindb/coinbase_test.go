package chaindb

import (
	"fmt"
	"math"
	"testing"
)

// TestBuildCoinbaseTxs：鏈上瓜分明細——M149o 三分發：節點 45%（出塊節點）＋池 15%＋礦工 40% 按算力瓜分（末位補齊）；
// 無在線礦工時節點 45% 給出塊節點、其餘 40%+15% 入池；多筆哈希唯一。
func TestBuildCoinbaseTxs(t *testing.T) {
	// 有礦工：a1 算力 1、a2 算力 3，reward=10 → 節點 4.5（proposer）、池 1.5、
	// 礦工 40%＝4 → a1=1、a2=3。
	txs := BuildCoinbaseTxs(5, "proposer", 12345,
		[]CoinbaseSplit{{Address: "a1", Share: 1}, {Address: "a2", Share: 3}}, 10)
	if len(txs) != 4 {
		t.Fatalf("交易數=%d 應為 4 (node+a1+a2+pool)", len(txs))
	}
	got := map[string]float64{}
	for _, tx := range txs {
		if tx.FromAddr != "" {
			t.Fatalf("coinbase from 應為空: %s", tx.FromAddr)
		}
		var v float64
		if _, err := fmt.Sscanf(tx.Amount, "%g", &v); err != nil {
			t.Fatal(err)
		}
		got[tx.ToAddr] = v
	}
	if math.Abs(got["proposer"]-4.5) > 1e-9 || math.Abs(got["a1"]-1) > 1e-9 ||
		math.Abs(got["a2"]-3) > 1e-9 || math.Abs(got[RewardPoolAddr]-1.5) > 1e-9 {
		t.Fatalf("瓜分錯誤: %+v", got)
	}
	// 恆等式：4.5＋1＋3＋1.5 == 10。
	sum := got["proposer"] + got["a1"] + got["a2"] + got[RewardPoolAddr]
	if math.Abs(sum-10) > 1e-9 {
		t.Fatalf("恆等式不成立: %g != 10", sum)
	}

	// 無礦工：節點 45% 給出塊節點、其餘 55%（40%+15%）入池（M36 語義）。
	txs2 := BuildCoinbaseTxs(6, "proposer", 12346, nil, 10)
	totalPool := 0.0
	nodeAmt := 0.0
	for _, tx := range txs2 {
		var v float64
		if _, err := fmt.Sscanf(tx.Amount, "%g", &v); err != nil {
			t.Fatal(err)
		}
		if tx.ToAddr == RewardPoolAddr {
			totalPool += v
		} else if tx.ToAddr == "proposer" {
			nodeAmt += v
		} else {
			t.Fatalf("無礦工收款人應只有獎勵池與出塊節點: %+v", tx)
		}
	}
	if math.Abs(totalPool-5.5) > 1e-9 {
		t.Fatalf("無礦工入池總額=%g want 5.5（40%%+15%%）", totalPool)
	}
	if math.Abs(nodeAmt-4.5) > 1e-9 {
		t.Fatalf("無礦工節點獎勵=%g want 4.5（45%%）", nodeAmt)
	}

	// 多筆哈希唯一（避免 merkle 衝突）。
	seen := map[string]bool{}
	for _, tx := range txs {
		if seen[tx.TxHash] {
			t.Fatalf("coinbase 哈希重複: %s", tx.TxHash)
		}
		seen[tx.TxHash] = true
	}
}
