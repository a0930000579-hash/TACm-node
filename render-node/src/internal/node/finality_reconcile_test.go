package node

// 最終化週期對齊：BFT 停滯且漏接 inline fallback 時，reconcileFinalizedFromTip
// 須依鏈頂保守把最終化推進到 tip-window+1，且只前進、不回退。

import (
	"fmt"
	"testing"
	"time"

	"tacm/internal/chaindb"
)

// seedLightBlocks 直接經 DB 插入輕量區塊（不走共識，避免時間槽等待），回傳鏈頂。
func seedLightBlocks(t *testing.T, n *Node, total int64) int64 {
	t.Helper()
	var prev string
	for h := int64(1); h <= total; h++ {
		hash := fmt.Sprintf("%064d", h)
		prevCopy := prev
		b := &chaindb.Block{
			Height: h, Hash: hash, PrevHash: &prevCopy,
			Proposer: "t", Ts: time.Now().Unix(),
		}
		if err := n.db.InsertBlock(b, nil); err != nil {
			t.Fatalf("InsertBlock %d: %v", h, err)
		}
		prev = hash
	}
	return n.db.GetTipHeight()
}

func TestFinalityReconcilerFromTip(t *testing.T) {
	n := benchNodeT(t, 1)
	total := int64(finalityFallbackWindow + 6)
	tip := seedLightBlocks(t, n, total)
	if tip != total {
		t.Fatalf("tip 應 %d，得 %d", total, tip)
	}

	// 落差小於視窗時不應推進。
	n.mu.Lock()
	n.finalizedH = tip - finalityFallbackWindow + 2
	before := n.finalizedH
	n.mu.Unlock()
	n.reconcileFinalizedFromTip()
	if got := n.FinalizedHeight(); got != before {
		t.Fatalf("視窗內不應推進: %d → %d", before, got)
	}

	// 落後過多時，reconcile 後應保守推進到 tip-window+1。
	n.mu.Lock()
	n.finalizedH = 1
	n.mu.Unlock()
	n.reconcileFinalizedFromTip()
	want := tip - finalityFallbackWindow + 1
	if got := n.FinalizedHeight(); got != want {
		t.Fatalf("reconcile 後 finalized 應 %d，得 %d", want, got)
	}

	// 再調一次不得回退、不得改變（冪等）。
	n.reconcileFinalizedFromTip()
	if got := n.FinalizedHeight(); got != want {
		t.Fatalf("冪等調用不應改變: %d → %d", want, got)
	}
}
