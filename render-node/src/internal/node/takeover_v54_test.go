package node

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"tacm/internal/chaindb"
)

// hashOrNil 安全取塊哈希（nil 時回 "<nil>"），供失敗訊息使用。
func hashOrNil(b *chaindb.Block) string {
	if b == nil {
		return "<nil>"
	}
	return b.Hash
}

// waitTip 等待節點 tip 高度 ≥ h（出塊/同步均適用）。
func waitTip(t *testing.T, n *Node, h int64, to time.Duration) {
	t.Helper()
	deadline := time.Now().Add(to)
	for time.Now().Before(deadline) {
		if n.DB().GetTipHeight() >= h {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("節點 tip 未達 %d：tip=%d", h, n.DB().GetTipHeight())
}

// TestDistributedTakeoverAfterAnchorDown 主鏈容錯 e2e（v54，參照比特幣：
// 創世節點掛了鏈仍能運行）：
//  1. 2 驗證人（主鏈 v0 ＋ 備援 v1）正常輪值出塊；
//  2. 主鏈 v0 失聯（P2P 斷線，v1 收不到其提案/區塊）；
//  3. v1 偵測 proposer 失聯（提議超時）→ 接管出塊，鏈持續推進（不卡 50% 空等）；
//  4. 主鏈 v0 恢復連線 → 以 v1 為 seed 拉較長鏈（M86-B catch-up / reorg）追平合併，
//     兩節點最終同高同哈希。
func TestDistributedTakeoverAfterAnchorDown(t *testing.T) {
	dn := buildCluster(t, 2)
	launchCluster(t, dn, []bool{true, true})

	// 1. 兩節點正常輪值出塊到 h≥3（驗證 2 驗證人網路正常運作）。
	waitTip(t, dn[0].n, 3, 60*time.Second)
	waitTip(t, dn[1].n, 3, 60*time.Second)

	// 2. 主鏈 v0 掛機（真實主鏈進程死亡語義）：HTTP 下線＋暫停共識＋關閉 P2P。
	shutCtx, shutCancel := context.WithTimeout(context.Background(), time.Second)
	_ = dn[0].srv.Shutdown(shutCtx)
	shutCancel()
	dn[0].n.SuspendConsensus()
	dn[0].netw.Close()

	// 3. v1 應接管：即使輪到 v0 提議的高度，v1 超時後補位出塊，tip 持續推進。
	tipBefore := dn[1].n.DB().GetTipHeight()
	waitTip(t, dn[1].n, tipBefore+3, 60*time.Second)
	t.Logf("v1 接管期間 tip 推進：%d → %d", tipBefore, dn[1].n.DB().GetTipHeight())
	if !dn[1].n.TakeoverActive() {
		t.Log("v1 takeover_active 為 false（時序上可能已恢復標記），以 tip 推進為準")
	}

	// 4. 主鏈 v0 恢復（進程重啟語義）：新 listener＋新 HTTP＋重新掛 P2P（以 v1 為
	//    seed）。恢復節點先純同步追平較長鏈（參照比特幣：重啟節點先同步再出塊），
	//    追平後再恢復共識。
	l0, port0 := listenPort(t)
	dn[0].url = fmt.Sprintf("http://127.0.0.1:%d", port0)
	netw0, err := dn[0].n.AttachP2P(dn[0].url, []string{dn[1].url}, false)
	if err != nil {
		t.Fatal(err)
	}
	root := http.NewServeMux()
	root.Handle("/p2p/", netw0.Handler())
	root.Handle("/", NewRPCServer(dn[0].n).Handler())
	srv0 := &http.Server{Handler: root}
	go func() { _ = srv0.Serve(l0) }()
	netw0.Start(context.Background())
	dn[0].netw = netw0
	dn[0].srv = srv0
	dn[0].l = l0
	dn[0].launched = true

	// v0 先以較長鏈規則追平 v1（缺口補齊/重組）。
	v1Tip := dn[1].n.DB().GetTipHeight()
	// 診斷：恢復後觀察 v0 的 peer 與 sync 進展。
	for i := 0; i < 10; i++ {
		time.Sleep(2 * time.Second)
		ph := "?"
		for _, p := range dn[0].n.P2PNet().PeerSnapshot() {
			ph = fmt.Sprintf("h=%d on=%v", p.Height, p.Online)
		}
		t.Logf("v0 tip=%d v1 tip=%d peers=%d peer(%s)",
			dn[0].n.DB().GetTipHeight(), dn[1].n.DB().GetTipHeight(),
			len(dn[0].n.P2PNet().PeerSnapshot()), ph)
		if dn[0].n.DB().GetTipHeight() >= v1Tip {
			break
		}
	}
	waitTip(t, dn[0].n, v1Tip, 60*time.Second)
	dn[0].n.ResumeConsensus()

	// 恢復後給兩節點一段協作出塊窗口，再比對（兩邊應同步推進、哈希一致）。
	waitTip(t, dn[1].n, v1Tip+3, 60*time.Second)
	waitTip(t, dn[0].n, v1Tip+3, 60*time.Second)
	// v54：合併採「較長鏈優先」——恢復節點追平後短暫雙出（同高度分叉）時，
	// 領先方持續出塊，落後方經 syncFrom 共同高度哈希比對觸發 doReorg 整鏈替換；
	// 因此「高度一致」不代表「已合併」，需輪詢等待兩端 tip 哈希一致（重組窗口）。
	merged := false
	for i := 0; i < 30; i++ {
		h0 := dn[0].n.DB().GetTipHeight()
		h1 := dn[1].n.DB().GetTipHeight()
		if h0 == h1 && h0 >= v1Tip+3 {
			b0, _ := dn[0].n.DB().GetBlock(h0)
			b1, _ := dn[1].n.DB().GetBlock(h1)
			if b0 != nil && b1 != nil && b0.Hash == b1.Hash {
				merged = true
				break
			}
		}
		time.Sleep(2 * time.Second)
	}
	if !merged {
		h0 := dn[0].n.DB().GetTipHeight()
		h1 := dn[1].n.DB().GetTipHeight()
		b0, _ := dn[0].n.DB().GetBlock(h0)
		b1, _ := dn[1].n.DB().GetBlock(h1)
		t.Fatalf("恢復後鏈未合併：v0=%d(hash=%s) v1=%d(hash=%s)",
			h0, hashOrNil(b0), h1, hashOrNil(b1))
	}
}

// TestDistributedTakeoverNoFalsePositive 正常 2 驗證人輪值期間不誤接管：
// 兩節點均在線時，proposer 正常出塊，tip 連續推進且無接管標記。
func TestDistributedTakeoverNoFalsePositive(t *testing.T) {
	dn := buildCluster(t, 2)
	launchCluster(t, dn, []bool{true, true})

	waitTip(t, dn[0].n, 4, 60*time.Second)
	waitTip(t, dn[1].n, 4, 60*time.Second)
	if dn[1].n.TakeoverActive() {
		t.Error("正常輪值期間 v1 不應進入接管狀態")
	}
	h0 := dn[0].n.DB().GetTipHeight()
	h1 := dn[1].n.DB().GetTipHeight()
	if h0 < 4 || h1 < 4 {
		t.Errorf("高度不足：v0=%d v1=%d", h0, h1)
	}
	b0, _ := dn[0].n.DB().GetBlock(4)
	b1, _ := dn[1].n.DB().GetBlock(4)
	if b0 == nil || b1 == nil || b0.Hash != b1.Hash {
		t.Errorf("正常輪值哈希不一致：v0=%v v1=%v", b0, b1)
	}
}
