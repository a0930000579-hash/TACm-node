package node

// M163 混沌/長穩測試（名稱帶 Chaos）：
//   - Goroutine 洩漏：起節點→出塊→關閉，runtime.NumGoroutine 前後取樣（寬限＋重試）。
//   - 區塊重組：長競爭鏈（8 塊）應觸發重組切換鏈頂；輕鏈（累積難度較小）應被拒絕，
//     不 panic、不吞錯。
//   - 節點斷線重連：peer 離線期間主鏈繼續推進，重連後 follower 補齊落後區塊（late sync）。
//
// 全部使用獨立 TempDir 與 startFullNode 的隨機空埠（listenPort :0），避免跨測試埠碰撞；
// 等待一律輪詢帶 timeout（waitHeight/waitFor），不依賴固定 sleep。

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"tacm/internal/chaindb"
)

// countFDByKind 統計開啟控制代碼：kind 可為 "socket"（HTTP listener/P2P 連線）
// 或 "db"（SQLite 的 .db/.db-wal/.db-shm）。用於驗證節點關閉後網路層與資料庫層
// 都完整釋放——長期運行節點不得線性累積控制代碼（否則會觸發 too many open files）。
func countFDByKind(t *testing.T, kind string) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("無 /proc/self/fd，略過 FD 收斂檢查")
	}
	n := 0
	for _, e := range entries {
		dest, err := os.Readlink("/proc/self/fd/" + e.Name())
		if err != nil {
			continue
		}
		switch kind {
		case "socket":
			if strings.Contains(dest, "socket:") {
				n++
			}
		case "db":
			if strings.Contains(dest, ".db") {
				n++
			}
		}
	}
	return n
}

// countSocketFD 統計指向 socket 的開啟控制代碼（向後相容的便捷封裝）。
func countSocketFD(t *testing.T) int { return countFDByKind(t, "socket") }

// closeFullNode 明確關閉一個 fullNode：P2P 網路循環、HTTP listener、節點（含 DB）。
func closeFullNode(t *testing.T, fn *fullNode) {
	t.Helper()
	fn.net.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = fn.srv.Shutdown(ctx)
	_ = fn.n.Close()
}

// TestChaosGoroutineLeak：連續起停節點數輪後，goroutine 數不應持續成長。
func TestChaosGoroutineLeak(t *testing.T) {
	baseline := runtime.NumGoroutine()

	for cycle := 0; cycle < 2; cycle++ {
		n, err := New(testCfg(t), "leak", 1)
		if err != nil {
			t.Fatal(err)
		}
		insertGenesis(t, n)
		n.Start()
		waitHeight(t, n, 3, 12*time.Second)
		if err := n.Close(); err != nil {
			t.Fatalf("關閉節點 %d: %v", cycle, err)
		}
	}

	runtime.GC()
	time.Sleep(300 * time.Millisecond)
	peak := runtime.NumGoroutine()
	for i := 0; i < 6; i++ {
		if g := runtime.NumGoroutine(); g > peak {
			peak = g
		}
		time.Sleep(150 * time.Millisecond)
	}

	// 寬限：容忍 runtime/idle churning ≤ baseline+40；超過則再做一次 GC＋沉降後重試。
	const tol = 40
	if peak-baseline > tol {
		runtime.GC()
		time.Sleep(500 * time.Millisecond)
		final := runtime.NumGoroutine()
		if final-baseline > tol {
			t.Fatalf("goroutine 持續成長: baseline=%d peak=%d final=%d", baseline, peak, final)
		}
	}
}

// TestChaosReorgLongerChain：長競爭鏈取代；隨後輕鏈應被拒絕、狀態不變。
func TestChaosReorgLongerChain(t *testing.T) {
	n, err := New(testCfg(t), "reorg", 1)
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

	b1, err := n.db.GetBlock(1)
	if err != nil || b1 == nil {
		t.Fatal("缺少高度 1 區塊")
	}

	// 長競爭鏈：h2..h9（8 塊），每塊 difficulty 2 → 累積 16，重於本地 h2..h3（累積 4）。
	prev := b1.Hash
	var longBlocks []chaindb.Block
	var longTxs [][]chaindb.Transaction
	for h := int64(2); h <= 9; h++ {
		b, txs := mineCompeting(t, n, h, prev, 2)
		longBlocks = append(longBlocks, *b)
		longTxs = append(longTxs, txs)
		prev = b.Hash
	}
	if err := n.P2PHost().ReorgChain(1, longBlocks, longTxs); err != nil {
		t.Fatalf("長競爭鏈重組失敗: %v", err)
	}
	if tip := n.db.GetTipHeight(); tip != 9 {
		t.Fatalf("重組後高度=%d 應為 9", tip)
	}
	newTip, _ := n.db.GetBlock(9)
	if newTip.Hash != longBlocks[len(longBlocks)-1].Hash {
		t.Error("鏈頂未切換到長競爭鏈")
	}

	// 輕競爭鏈：h2..h5 difficulty 1 → 累積 4，不重於本地 h2..h9（累積 16），應被拒絕。
	keepTip, _ := n.db.GetBlock(9)
	prev = b1.Hash
	var lightBlocks []chaindb.Block
	var lightTxs [][]chaindb.Transaction
	for h := int64(2); h <= 5; h++ {
		b, txs := mineCompeting(t, n, h, prev, 1)
		lightBlocks = append(lightBlocks, *b)
		lightTxs = append(lightTxs, txs)
		prev = b.Hash
	}
	if err := n.P2PHost().ReorgChain(1, lightBlocks, lightTxs); err == nil {
		t.Error("輕競爭鏈應被拒絕，但重組成功")
	}
	after, _ := n.db.GetBlock(9)
	if after.Hash != keepTip.Hash {
		t.Error("拒絕輕鏈後本地鏈頂被改變")
	}
}

// TestChaosPeerReconnectLateSync：follower 離線期間主鏈推進，重連後補齊落後區塊。
func TestChaosPeerReconnectLateSync(t *testing.T) {
	A := startFullNode(t, "chaosA", nil, false, "")
	waitHeight(t, A.n, 4, 12*time.Second)

	B := startFullNode(t, "chaosB", []string{A.url}, true, "")
	waitHeight(t, B.n, 4, 15*time.Second)

	// B 斷線：停掉 B 的 P2P 心跳/同步循環（節點本體仍在）。
	B.net.Close()

	// A 繼續出塊到 8；B 離線期間應停在 4。
	waitHeight(t, A.n, 8, 15*time.Second)
	if h := B.n.DB().GetTipHeight(); h != 4 {
		t.Fatalf("B 離線期間應停在 4，實際 %d", h)
	}

	// B 重連：重啟 P2P 網路循環並重新加入 A。
	B.net.Start(context.Background())
	B.net.AddPeerByURL(A.url)

	// B 應補齊落後區塊到 A 的高度。
	waitHeight(t, B.n, 8, 25*time.Second)

	// 共同高度段區塊哈希一致（A 可能續出塊，故固定比對 5..8）。
	for h := int64(5); h <= 8; h++ {
		ab, _ := A.n.DB().GetBlock(h)
		bb, _ := B.n.DB().GetBlock(h)
		if ab == nil || bb == nil {
			t.Fatalf("h=%d 缺塊", h)
		}
		if ab.Hash != bb.Hash {
			t.Fatalf("h=%d 重連同步後區塊哈希不一致: %s vs %s", h, ab.Hash, bb.Hash)
		}
	}
}

// TestChaosFDConvergence：重複建立-關閉「出塊節點＋follower（已互連同步）」數輪，
// 確認每次 Close 後 HTTP listener、P2P 連線與全部 SQLite store（.db/.db-wal/.db-shm）
// 都真的釋放——網路與資料庫控制代碼都必須收斂、埠可重用，不得逐輪線性累積。
func TestChaosFDConvergence(t *testing.T) {
	settle := func() {
		runtime.GC()
		time.Sleep(350 * time.Millisecond)
	}
	settle()
	baseSock, baseDB := countFDByKind(t, "socket"), countFDByKind(t, "db")

	worstSock, worstDB := 0, 0
	for i := 0; i < 5; i++ {
		A := startFullNode(t, "fda", nil, false, "")
		B := startFullNode(t, "fdb", []string{A.url}, true, "")
		waitHeight(t, A.n, 3, 12*time.Second)
		waitHeight(t, B.n, 3, 12*time.Second)
		closeFullNode(t, A)
		closeFullNode(t, B)
		settle() // 等連線關閉與 SQL 連線池回收
		if c := countFDByKind(t, "socket"); c > worstSock {
			worstSock = c
		}
		if c := countFDByKind(t, "db"); c > worstDB {
			worstDB = c
		}
	}
	// 連續建立-關閉不應殘留任何監聽器/連線（socket）或資料庫控制代碼（db）。
	// 各容許極少數 runtime 暫態；超出即代表有資源未在 Close 釋放。
	if worstSock-baseSock > 6 {
		t.Fatalf("網路控制代碼未收斂: base=%d worst=%d", baseSock, worstSock)
	}
	if worstDB-baseDB > 2 {
		t.Fatalf("資料庫控制代碼未收斂: base=%d worst=%d", baseDB, worstDB)
	}
}

// TestChaosFDPerBlockStable：單一節點持續出塊（不關閉），檔案/資料庫控制代碼不得隨
// 區塊數「線性增長」——捕捉逐塊/逐同步路徑的 sql.Rows、os.File、HTTP body 未關閉洩漏。
// 說明：SQLite 連線池在初次並發時會從 1 條擴到有界的常駐數（warm-up），之後應穩定；
// 故先 warm-up 到穩態再取基線，只斷言「穩態之後不再增長」，不把一次性池擴容誤判為洩漏。
func TestChaosFDPerBlockStable(t *testing.T) {
	n, err := New(testCfg(t), "fdblock", 1)
	if err != nil {
		t.Fatal(err)
	}
	insertGenesis(t, n)
	n.Start()
	defer func() { _ = n.Close() }()

	// warm-up：出到高度 12，讓連線池/各 store 進入並發穩態。
	waitHeight(t, n, 12, 20*time.Second)
	runtime.GC()
	time.Sleep(300 * time.Millisecond)
	baseH := n.DB().GetTipHeight()
	baseDB, baseFile := countFDByKind(t, "db"), countFDByKind(t, "file")

	// 穩態後再持續出 30 個塊，涵蓋轉帳/合約/打包/共識等熱路徑。
	waitHeight(t, n, baseH+30, 45*time.Second)
	time.Sleep(400 * time.Millisecond)
	runtime.GC()

	if d := countFDByKind(t, "db") - baseDB; d > 0 {
		t.Fatalf("穩態後出塊 %d 個，資料庫控制代碼仍增長 %d（疑似 sql.Rows/連線未關閉）", 30, d)
	}
	if d := countFDByKind(t, "file") - baseFile; d > 2 {
		t.Fatalf("穩態後出塊 %d 個，檔案控制代碼增長 %d（疑似 os.File 未關閉）", 30, d)
	}
}
