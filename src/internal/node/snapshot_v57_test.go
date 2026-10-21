package node

// v57：快照信任驗證（主流鏈 fast-sync 同級）——
//  1. 空節點導入合法快照：創世＋tip 對齊、秒級就緒；
//  2. 創世 hash 不匹配 → 拒絕導入（防誤導別鏈快照造成分叉）；
//  3. tip hash 欄位被篡改 → 拒絕（防篡改）；
//  4. 快照高度倒退 → 拒絕（防覆蓋更新的鏈）。

import (
	"strings"
	"testing"
	"time"
)

// makeSnapshotSrc 建立出 2 塊的來源節點並導出快照。
func makeSnapshotSrc(t *testing.T) (*Node, *SnapshotData) {
	t.Helper()
	src := benchNodeT(t, 1)
	if err := src.produceBlock(); err != nil {
		t.Fatalf("出塊1: %v", err)
	}
	if err := src.produceBlock(); err != nil {
		t.Fatalf("出塊2: %v", err)
	}
	sd, err := src.ExportSnapshot()
	if err != nil {
		t.Fatalf("ExportSnapshot: %v", err)
	}
	if sd == nil || sd.GenesisBlock == nil || sd.TipBlock == nil {
		t.Fatalf("快照不完整: %+v", sd)
	}
	if sd.Height != 2 {
		t.Fatalf("快照高度應 2，得 %d", sd.Height)
	}
	return src, sd
}

// TestSnapshotTrust_ImportFreshOk 空節點導入合法快照：創世/tip 對齊、可繼續出塊。
func TestSnapshotTrust_ImportFreshOk(t *testing.T) {
	_, sd := makeSnapshotSrc(t)
	cfg := testCfg(t)
	n, err := New(cfg, "node1", 1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = n.Close() }()
	if err := n.ImportSnapshot(sd); err != nil {
		t.Fatalf("導入合法快照不應失敗: %v", err)
	}
	if got := n.db.GetTipHeight(); got != sd.Height {
		t.Fatalf("導入後 tip 應 %d，得 %d", sd.Height, got)
	}
	g, err := n.db.GetBlock(0)
	if err != nil || g == nil {
		t.Fatalf("導入後創世缺失: %v", err)
	}
	if g.Hash != sd.GenesisHash {
		t.Fatalf("創世 hash 應 %s，得 %s", sd.GenesisHash, g.Hash)
	}
	// 導入後可正常出塊（接 tip 續鏈）。
	if err := n.produceBlock(); err != nil {
		t.Fatalf("導入後出塊失敗: %v", err)
	}
	if got := n.db.GetTipHeight(); got != sd.Height+1 {
		t.Fatalf("續鏈後 tip 應 %d，得 %d", sd.Height+1, got)
	}
}

// TestSnapshotTrust_GenesisMismatch 本地已有創世且與快照創世 hash 不符 → 拒絕導入。
func TestSnapshotTrust_GenesisMismatch(t *testing.T) {
	_, sd := makeSnapshotSrc(t)
	// 模擬別鏈快照：篡改創世 hash（本地創世與此必然不同）。
	sd.GenesisHash = strings.Repeat("ff", 32)
	sd.GenesisBlock.Hash = strings.Repeat("ff", 32)
	// 本地節點：已有自己的創世。
	n := benchNodeT(t, 1)
	err := n.ImportSnapshot(sd)
	if err == nil {
		t.Fatalf("創世不匹配應拒絕導入")
	}
	if !strings.Contains(err.Error(), "創世") {
		t.Fatalf("錯誤訊息應指創世不匹配，得: %v", err)
	}
	if got := n.db.GetTipHeight(); got != 0 {
		t.Fatalf("拒絕後本地鏈不應被改寫，tip=%d", got)
	}
}

// TestSnapshotTrust_TipHashTamper tip hash 欄位與 tip 區塊 hash 不符 → 拒絕導入。
func TestSnapshotTrust_TipHashTamper(t *testing.T) {
	_, sd := makeSnapshotSrc(t)
	sd.TipHash = strings.Repeat("00", 32)
	cfg := testCfg(t)
	n, err := New(cfg, "node1", 1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = n.Close() }()
	err = n.ImportSnapshot(sd)
	if err == nil {
		t.Fatalf("tip hash 篡改應拒絕導入")
	}
	if !strings.Contains(err.Error(), "tip hash") {
		t.Fatalf("錯誤訊息應指 tip hash 不符，得: %v", err)
	}
}

// TestSnapshotTrust_HeightRegression 本地高度高於快照 → 拒絕導入（防覆蓋更新的鏈）。
func TestSnapshotTrust_HeightRegression(t *testing.T) {
	_, sd := makeSnapshotSrc(t)
	n := benchNodeT(t, 1)
	// 本地出 5 塊（高於快照 height=2）。
	for i := 0; i < 5; i++ {
		if err := n.produceBlock(); err != nil {
			t.Fatalf("本地出塊: %v", err)
		}
	}
	if got := n.db.GetTipHeight(); got != 5 {
		t.Fatalf("本地 tip 應 5，得 %d", got)
	}
	err := n.ImportSnapshot(sd)
	if err == nil {
		t.Fatalf("高度倒退應拒絕導入")
	}
	if !strings.Contains(err.Error(), "高於") {
		t.Fatalf("錯誤訊息應指高度倒退，得: %v", err)
	}
	if got := n.db.GetTipHeight(); got != 5 {
		t.Fatalf("拒絕後本地鏈不應被覆蓋，tip=%d", got)
	}
	_ = time.Now()
}
