package node

import (
	"testing"
	"time"
)

// TestReconcileWalletCatchesUpAfterCursorFallback 驗證錢包游標在重組後落後（卡住）時，
// reconcileWallet 能把錢包從舊游標補齊到鏈頂，而非永久停滯空轉。
func TestReconcileWalletCatchesUpAfterCursorFallback(t *testing.T) {
	n, err := New(testCfg(t), "wrec", 1)
	if err != nil {
		t.Fatal(err)
	}
	insertGenesis(t, n)
	n.Start()
	t.Cleanup(func() { _ = n.Close() })

	waitFor(t, func() bool { return n.DB().GetTipHeight() >= 3 }, 30*time.Second, "等待出塊到 h3")
	tip := n.DB().GetTipHeight()

	// 正常情況錢包應已追平鏈頂。
	before, err := n.Wallet().SyncedHeight()
	if err != nil {
		t.Fatalf("讀取同步高度失敗: %v", err)
	}
	if before < tip {
		t.Fatalf("錢包初始同步高度=%d 應 ≥ 鏈頂=%d", before, tip)
	}

	// 模擬重組：把錢包游標強制回退到高度 1（鏈頂資料仍在，僅錢包派生視圖落後）。
	if err := n.Wallet().MarkSynced(1); err != nil {
		t.Fatalf("回退錢包游標失敗: %v", err)
	}
	stuck, err := n.Wallet().SyncedHeight()
	if err != nil {
		t.Fatalf("讀取回退後高度失敗: %v", err)
	}
	if stuck != 1 {
		t.Fatalf("回退後游標=%d 應為 1", stuck)
	}

	// 觸發對齊，應把錢包補回鏈頂。
	n.reconcileWallet()
	after, err := n.Wallet().SyncedHeight()
	if err != nil {
		t.Fatalf("對齊後讀取高度失敗: %v", err)
	}
	if after < tip {
		t.Fatalf("對齊後錢包高度=%d 應 ≥ 鏈頂=%d", after, tip)
	}
}

// TestReconcileWalletIdempotentWhenCaughtUp 驗證錢包已追平時重複呼叫為安全空操作，
// 不會改變游標、也不報錯。
func TestReconcileWalletIdempotentWhenCaughtUp(t *testing.T) {
	n, err := New(testCfg(t), "wrec2", 1)
	if err != nil {
		t.Fatal(err)
	}
	insertGenesis(t, n)
	n.Start()
	t.Cleanup(func() { _ = n.Close() })

	waitFor(t, func() bool { return n.DB().GetTipHeight() >= 2 }, 30*time.Second, "等待出塊到 h2")
	tip := n.DB().GetTipHeight()

	n.reconcileWallet()
	n.reconcileWallet()
	h, err := n.Wallet().SyncedHeight()
	if err != nil {
		t.Fatalf("讀取高度失敗: %v", err)
	}
	if h < tip {
		t.Fatalf("冪等呼叫後錢包高度=%d 應 ≥ 鏈頂=%d", h, tip)
	}
}
