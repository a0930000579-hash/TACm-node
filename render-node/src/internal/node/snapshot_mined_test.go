package node

// 快照必須帶出全鏈累計已發 TACm，並在導入端還原 chain_stats；否則空庫 follower
// 的 mined 計數為 0，ComputeCappedReward 不做硬上限截斷，追塊 coinbase 校驗誤判。

import (
	"math/big"
	"testing"

	"tacm/internal/chaindb"
)

func TestSnapshotCarriesMinedAndRestores(t *testing.T) {
	src := benchNodeT(t, 1)
	for i := 0; i < 4; i++ {
		if err := src.produceBlock(); err != nil {
			t.Fatalf("出塊 %d: %v", i+1, err)
		}
	}
	srcMined, err := src.walletSvc.ChainMinedTacm()
	if err != nil {
		t.Fatalf("src ChainMinedTacm: %v", err)
	}
	sd, err := src.ExportSnapshot()
	if err != nil {
		t.Fatalf("ExportSnapshot: %v", err)
	}
	if sd.MinedTacmWei != srcMined.String() {
		t.Fatalf("快照 mined=%q 應為 %q", sd.MinedTacmWei, srcMined.String())
	}

	cfg := testCfg(t)
	n, err := New(cfg, "node-follower", 1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = n.Close() }()
	if err := n.ImportSnapshot(sd); err != nil {
		t.Fatalf("ImportSnapshot: %v", err)
	}
	gotMined, err := n.walletSvc.ChainMinedTacm()
	if err != nil {
		t.Fatalf("dst ChainMinedTacm: %v", err)
	}
	if gotMined.Cmp(srcMined) != 0 {
		t.Fatalf("導入後 mined=%s 應為 %s", gotMined.String(), srcMined.String())
	}

	// 導入端與源端對下一個高度的 capped reward 必須一致（截斷口徑同源）。
	next := sd.Height + 1
	want := chaindb.ComputeCappedReward(next, int64(n.cfg.BlockTime), gotMined)
	srcWant := chaindb.ComputeCappedReward(next, int64(src.cfg.BlockTime), srcMined)
	if abs(want-srcWant) > 1e-9 {
		t.Fatalf("h=%d dst capped=%g src capped=%g 不一致", next, want, srcWant)
	}
}

// TestSnapshotIncompleteHistorySkipsReplay：healWalletSync 在缺第 1 塊（快照裁剪）時
// 不得用本地 coinbase 重放覆蓋快照寫入的權威累計量。
func TestSnapshotIncompleteHistorySkipsReplay(t *testing.T) {
	src := benchNodeT(t, 1)
	if err := src.produceBlock(); err != nil {
		t.Fatalf("出塊: %v", err)
	}
	sd, err := src.ExportSnapshot()
	if err != nil {
		t.Fatalf("ExportSnapshot: %v", err)
	}
	cfg := testCfg(t)
	n, err := New(cfg, "node2", 1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = n.Close() }()
	if err := n.ImportSnapshot(sd); err != nil {
		t.Fatalf("ImportSnapshot: %v", err)
	}
	n.healWalletSync()
	got, err := n.walletSvc.ChainMinedTacm()
	if err != nil {
		t.Fatalf("ChainMinedTacm: %v", err)
	}
	want, _ := new(big.Int).SetString(sd.MinedTacmWei, 10)
	if got.Cmp(want) != 0 {
		t.Fatalf("heal 後 mined=%s 應保留快照值 %s", got.String(), want.String())
	}
}
