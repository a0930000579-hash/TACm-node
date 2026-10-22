package node

import "testing"

// TestFinalityFallback：BFT 停滯（驗證節點失聯／未達 quorum）時，
// 視窗最終化 fallback 應依「視窗無重組」保守推進；未達視窗不動；
// 已推進後落在視窗內不再重複推進。
func TestFinalityFallback(t *testing.T) {
	n := &Node{}
	n.finalizedH = 100 // 模擬 BFT 停滯於 100

	// 1) 落後不足視窗（256）：不推進
	n.applyFinalityFallback(300)
	if got := n.FinalizedHeight(); got != 100 {
		t.Fatalf("未達視窗不應推進：got %d want 100", got)
	}

	// 2) 落後 ≥ 視窗：推進到 tip-255（此處無 db，tip 視同 h=400）
	n.applyFinalityFallback(400)
	if got := n.FinalizedHeight(); got != 145 {
		t.Fatalf("視窗推進錯誤：got %d want 145 (400-255)", got)
	}

	// 3) 差距回到視窗內（254 < 256）：不動
	n.applyFinalityFallback(399)
	if got := n.FinalizedHeight(); got != 145 {
		t.Fatalf("視窗內不應重複推進：got %d want 145", got)
	}

	// 4) 持續出塊繼續推進
	n.applyFinalityFallback(800)
	if got := n.FinalizedHeight(); got != 545 {
		t.Fatalf("持續推進錯誤：got %d want 545 (800-255)", got)
	}

	// 5) h 低於 finalized：不動（防倒退）
	n.applyFinalityFallback(500)
	if got := n.FinalizedHeight(); got != 545 {
		t.Fatalf("不應倒退：got %d want 545", got)
	}
}
