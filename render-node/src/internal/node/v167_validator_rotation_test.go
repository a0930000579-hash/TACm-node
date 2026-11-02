package node

// v167 任務 3：Render 節點從 follower 改為驗證人輪流出塊 e2e。
//  - TestV167ValidatorProposerRotation：兩個鏈上質押驗證人 → BFT proposer 依高度確定性
//    輪替（round-robin），連續高度 proposer 嚴格交替。
//  - TestV167TwoValidatorsProduceBlocks：以 StartDistributed（移除 -p2p-follower 後的路徑）
//    啟動兩驗證人 → 二者輪流出塊推進 tip，鏈哈希一致（驗證人輪流出塊真實運作）。

import (
	"testing"
	"time"
)

// TestV167ValidatorProposerRotation 兩驗證人輪值 proposer 確定性輪替：
// 同權重兩驗證人時，Proposer(h) = validators[h%2]，連續高度必為不同驗證人。
func TestV167ValidatorProposerRotation(t *testing.T) {
	dn := buildCluster(t, 2)
	v56StakeAll(t, dn, 2000)
	for _, d := range dn {
		if err := d.n.RebuildValidatorSetFromPOS(); err != nil {
			t.Fatal(err)
		}
	}
	v0, v1 := dn[0].n, dn[1].n
	if v0.vset == nil || v0.vset.Len() != 2 {
		t.Fatalf("兩質押驗證人入集失敗，vset.Len=%d", v0.vset.Len())
	}
	// 兩節點對同一條鏈的視圖必須一致（proposer 輪替基準相同）。
	if v0.vset.Len() != v1.vset.Len() {
		t.Fatal("兩節點 vset 大小不一致")
	}

	seen := map[string]int{}
	var prev string
	alternating := true
	for h := int64(1); h <= 8; h++ {
		p := v0.vset.Proposer(h, 0)
		if p == nil {
			t.Fatalf("height %d 無 proposer", h)
		}
		seen[p.Address]++
		if prev != "" && p.Address == prev {
			alternating = false
		}
		prev = p.Address
	}
	// 兩驗證人都應輪到。
	if len(seen) != 2 {
		t.Fatalf("輪值 proposer 只覆蓋 %d 個驗證人（應 2）：%v", len(seen), seen)
	}
	if !alternating {
		t.Fatalf("連續高度 proposer 未交替：%v", seen)
	}
	t.Logf("proposer 輪替計數：%v", seen)
}

// TestV167TwoValidatorsProduceBlocks 移除 follower 標誌後（StartDistributed 路徑），
// 兩驗證人真實輪流出塊、推進 tip、鏈哈希跨節點一致。
func TestV167TwoValidatorsProduceBlocks(t *testing.T) {
	dn := buildCluster(t, 2)
	v56StakeAll(t, dn, 2000)
	for _, d := range dn {
		if err := d.n.RebuildValidatorSetFromPOS(); err != nil {
			t.Fatal(err)
		}
	}
	// launchCluster → StartDistributed（= cmd/nodeweb 移除 -p2p-follower 後的啟動分支）。
	launchCluster(t, dn, []bool{true, true})

	// 等待鏈推進到 h≥5（兩驗證人輪流出塊）。
	waitTip(t, dn[0].n, 5, 90*time.Second)
	waitTip(t, dn[1].n, 5, 90*time.Second)

	// 收集 v0 鏈上各高度實際 proposer，斷言兩驗證人都出過塊（輪流提議）。
	proposers := map[string]bool{}
	for h := int64(1); h <= 5; h++ {
		b, err := dn[0].n.DB().GetBlock(h)
		if err != nil || b == nil {
			t.Fatalf("缺少高度 %d: %v", h, err)
		}
		who := b.ProposerAddress
		if who == "" {
			who = b.Proposer
		}
		proposers[who] = true
	}
	if !proposers[dn[0].n.nodeAddress] || !proposers[dn[1].n.nodeAddress] {
		t.Fatalf("兩驗證人應輪流出塊，實際 proposers=%v", proposers)
	}

	// 鏈跨節點一致（同高同哈希）。
	h := dn[0].n.DB().GetTipHeight()
	b0, _ := dn[0].n.DB().GetBlock(h)
	b1, _ := dn[1].n.DB().GetBlock(h)
	if b0 == nil || b1 == nil || b0.Hash != b1.Hash {
		t.Fatalf("兩節點 tip 哈希不一致：v0=%v v1=%v", b0, b1)
	}
	t.Logf("兩驗證人輪流出塊成功：proposers=%v tip=%d", proposers, h)
}
