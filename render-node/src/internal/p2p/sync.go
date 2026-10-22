package p2p

import (
	"log"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strconv"
	"time"

	"tacm/internal/chaindb"
)

func (n *Network) syncLoop(ctx context.Context) {
	defer n.wg.Done()
	// M149：失敗退避——連不上（閘道/對端防護）時拉長重試間隔，
	// 避免「每 2 秒固定重試」觸發雲平台/IP-Pool 動態防護（會連帶封鎖雲端節點 IP）。
	base := n.cfg.SyncInterval
	if base < 10*time.Second {
		base = 10 * time.Second
	}
	interval := base
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// M122：落後較多時密集追齊（不等下一 tick），直到追平或無進展；
			// 上限 20 輪×100ms，避免阻塞 loop。
			progress := false
			for i := 0; i < 20; i++ {
				before := n.host.Height()
				if n.syncOnce() {
					progress = true
				}
				if n.host.Height() >= n.bestPeerHeight() || n.host.Height() == before {
					break
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(100 * time.Millisecond):
				}
			}
			// 退避調節：有進展→回到基準；無進展（連不上/被防護）→ 一步退到 60s。
			// 逐級翻倍會讓前幾輪（10s/20s/40s）仍持續打對端，維持雲端防護清洗；
			// 一步到位後每分鐘最多 1 個請求，防護自然解除。
			if progress {
				interval = base
			} else {
				// v54：無進展時先主動刷新對端高度快照（心跳 15s 滯後，恢復節點
				// 可能因快照未更新而誤判「已追平」）再試一次；仍無進展才退避。
				// 追平窗口由此縮短到單個 tick，避免「主鏈恢復→追平」拖到 60s+。
				n.refreshPeerHeights()
				if n.syncOnce() {
					interval = base
				} else {
					interval = 60 * time.Second
				}
			}
			ticker.Reset(interval)
		}
	}
}

// bestPeerHeight 回傳在線最高 peer 高度；無 peer 時回傳自身高度（視為已追平）。
func (n *Network) bestPeerHeight() int64 {
	best, ok := n.bestPeer()
	if !ok {
		return n.host.Height()
	}
	return best.Height
}

// bestPeer 返回在線且鏈頂最高的節點（值快照）。
func (n *Network) bestPeer() (Peer, bool) {
	var best Peer
	found := false
	for _, p := range n.onlinePeers() {
		if !found || p.Height > best.Height {
			best, found = p, true
		}
	}
	return best, found
}

// bestPeers M122：回傳在線節點按高度降序的前 limit 個（值快照）。
// 用於多 peer 容錯：最佳 peer 不可達時依序嘗試次佳。
func (n *Network) bestPeers(limit int) []Peer {
	ps := n.onlinePeers()
	if len(ps) == 0 {
		return nil
	}
	// 簡單選擇排序取 top-limit（peer 數通常很小）。
	for i := 0; i < len(ps)-1; i++ {
		mi := i
		for j := i + 1; j < len(ps); j++ {
			if ps[j].Height > ps[mi].Height {
				mi = j
			}
		}
		ps[i], ps[mi] = ps[mi], ps[i]
		if i+1 >= limit {
			break
		}
	}
	if len(ps) > limit {
		ps = ps[:limit]
	}
	return ps
}

// sameGenesisTx 比較兩條創世的交易內容（from/to/amount），用於識別「哈希相同但內容不同」的創世。
func sameGenesisTx(a, b []chaindb.Transaction) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].FromAddr != b[i].FromAddr || a[i].ToAddr != b[i].ToAddr || a[i].Amount != b[i].Amount {
			return false
		}
	}
	return true
}

// syncOnce 嘗試追齊；回傳 true 表示本輪有實際進展（任一 peer 完成拉取）。
func (n *Network) syncOnce() bool {
	// M149d：非阻塞鎖——上次同步未完成（如整鏈重放 14000 塊）時，缺口觸發直接跳過，
	// 根治 Render 每 2-3 秒一塊就疊加一次完整重放的 OOM 重啟循環。
	if !n.syncMu.TryLock() {
		log.Printf("[p2p] 同步已在進行中，跳過本次缺口觸發")
		return false
	}
	defer n.syncMu.Unlock()
	// M122：依序嘗試 top-3 在線 peer——最佳 peer 拉取失敗（離線/瞬斷）時
	// 自動 fallback 次佳，不再整輪空轉等下一 tick。
	for _, p := range n.bestPeers(3) {
		if n.syncFrom(p) {
			return true
		}
	}
	return false
}

// syncFrom 從單一 peer 追齊；回傳 true 表示該 peer 可達並完成本輪嘗試
// （已追平、已觸發重組、或已無缺口），false 表示該 peer 不可用（嘗試下一 peer）。
func (n *Network) syncFrom(p Peer) bool {
	self := n.host.Height()
	if p.Height <= self {
		return false
	}

	// 比較共同高度 self 的區塊哈希，判斷是線性落後還是分叉。
	if self >= 0 {
		pb, pbTxs, perr := n.fetchPeerBlock(p, self)
		lb, lbTxs, lerr := n.host.BlockDetail(self)
		if perr != nil || pb == nil {
			return false
		}
		// 本機無此高度區塊（空鏈或缺塊）：走重組；doReorg 在無共同祖先時整鏈替換。
		if lerr != nil || lb == nil {
			n.doReorg(p, self)
			return true
		}
		// M149f：創世哈希是難度 0 固定值（000000000000…），哈希相同不代表內容相同；
		// self==0 時額外比較創世交易（社群基金地址），不同仍走整鏈替換。
		genesisDiff := pb.Hash != lb.Hash || (self == 0 && !sameGenesisTx(pbTxs, lbTxs))
		if genesisDiff {
			log.Printf("[p2p] 創世不同（哈希/內容）→ 整鏈替換（本機 %s vs 錨點 %s）", lb.Hash[:12], pb.Hash[:12])
			n.doReorg(p, self)
			return true
		}
		log.Printf("[p2p] 創世一致 → 線性追 %d..%d", self+1, p.Height)
	}

	// M121：線性追塊——缺口分片並行拉取（Goroutine），按高度合併後順序接入；
	// 批次內某塊接入失敗（含臨時缺口）時降級為單塊重試，不再整輪中止。
	// 串行（拉取為瓶頸時並行化）→ 單片時直接一次批量拉取，避免 goroutine 開銷。
	blocks, ok := n.fetchBlocksParallel(p, self+1, p.Height)
	if !ok {
		return false
	}
	for _, bd := range blocks {
		if err := n.host.OnIncomingBlock(bd.Block, bd.Txs); err != nil {
			// 缺口/驗證失敗：單塊再試一次；仍失敗則中止本輪（等下一 tick）。
			log.Printf("[p2p] 線性追接入失敗 h=%d（首試）: %v；單塊重試", bd.Block.Height, err)
			sb, stxs, serr := n.fetchPeerBlock(p, bd.Block.Height)
			if serr != nil || sb == nil {
				log.Printf("[p2p] 線性追重試拉取失敗 h=%d: %v", bd.Block.Height, serr)
				return false
			}
			if err := n.host.OnIncomingBlock(sb, stxs); err != nil {
				log.Printf("[p2p] 線性追接入失敗 h=%d（重試後仍失敗）: %v", sb.Height, err)
				return false
			}
		}
	}
	return true
}

// fetchBlocksParallel M121：分片並行拉取 [from, to] 區塊。
// 每片高度範圍 ≤500（與 /p2p/blocks 單批上限一致），Goroutine 並行發起批量請求，
// 收集後按高度升序合併回傳；任一片失敗回傳 ok=false（呼叫端可降級逐塊重試）。
// 片數為 1 時直接單次批量拉取，避免無謂的 goroutine/管道開銷。
func (n *Network) fetchBlocksParallel(p Peer, from, to int64) ([]blockData, bool) {
	total := to - from + 1
	if total <= 0 {
		return nil, true
	}
	const maxBatch = 500

	type seg struct{ from, to int64 }
	var segs []seg
	for h := from; h <= to; h += maxBatch {
		hi := h + maxBatch - 1
		if hi > to {
			hi = to
		}
		segs = append(segs, seg{h, hi})
	}
	if len(segs) == 1 {
		batch, err := n.fetchPeerBlocks(p, from, to)
		if err != nil {
			return nil, false
		}
		return batch, true
	}

	log.Printf("[p2p] 同步拉取：分 %d 段並行拉取 %d..%d（每段上限 500 塊）", len(segs), from, to)
	workers := runtime.GOMAXPROCS(0)
	if workers > 4 {
		workers = 4
	}
	type segResult struct {
		blocks []blockData
		err    error
	}
	results := make(chan segResult, len(segs))
	sem := make(chan struct{}, workers)
	for _, sg := range segs {
		go func(sg seg) {
			sem <- struct{}{}
			defer func() { <-sem }()
			batch, err := n.fetchPeerBlocks(p, sg.from, sg.to)
			results <- segResult{blocks: batch, err: err}
		}(sg)
	}

	all := make(map[int64]blockData, total)
	for i := range segs {
		r := <-results
		if r.err != nil {
			log.Printf("[p2p] 同步拉取失敗（段 %d/%d）: %v", i+1, len(segs), r.err)
			return nil, false
		}
		log.Printf("[p2p] 同步拉取：段 %d/%d 完成（%d 塊）", i+1, len(segs), len(r.blocks))
		for _, bd := range r.blocks {
			all[bd.Block.Height] = bd
		}
	}
	out := make([]blockData, 0, total)
	for h := from; h <= to; h++ {
		bd, ok := all[h]
		if !ok {
			return nil, false
		}
		out = append(out, bd)
	}
	return out, true
}

// fetchPeerBlocks M86：向 best 節點批量拉取 [from, best.Height] 區塊（上限 500/批）。
func (n *Network) fetchPeerBlocks(p Peer, from, to int64) ([]blockData, error) {
	if n.ctx == nil {
		return nil, fmt.Errorf("p2p: not started")
	}
	url := p.BaseURL + "/p2p/blocks?from=" + strconv.FormatInt(from, 10) +
		"&to=" + strconv.FormatInt(to, 10)
	req, err := http.NewRequestWithContext(n.ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch blocks from %d status %d", from, resp.StatusCode)
	}
	var d struct {
		Blocks []blockData `json:"blocks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, err
	}
	return d.Blocks, nil
}

// doReorg 回退找到共同分叉點，拉取對方分叉後全部區塊並交節點重組。
func (n *Network) doReorg(best Peer, from int64) {
	fork := int64(-1)
	for h := from; h >= 0; h-- {
		pb, pbTxs, perr := n.fetchPeerBlock(best, h)
		lb, lbTxs, lerr := n.host.BlockDetail(h)
		if perr != nil || pb == nil || lerr != nil || lb == nil {
			continue
		}
		if pb.Hash == lb.Hash {
			// M149h：創世哈希是難度 0 固定值，相同不代表內容相同；內容不同不視為共同祖先。
			if h == 0 && !sameGenesisTx(pbTxs, lbTxs) {
				continue
			}
			fork = h
			break
		}
	}
	if fork < 0 {
		// 創世都不一致：信任 best peer 的整條鏈，整鏈替換（含創世 #0）。
		n.reorgFullChain(best)
		return
	}

	// M149h：fork≥0 重組——分批並行拉取 fork+1..best（取代逐塊串行 HTTP 瓶頸），
	// 再每 500 塊一段重放（與整鏈替換同模式），進度逐段 log。
	blocks, ok := n.fetchBlocksParallel(best, fork+1, best.Height)
	if !ok {
		log.Printf("[p2p] 重組拉取失敗 fork=%d（分批並行，此輪放棄）", fork)
		return
	}
	all := make([]chaindb.Block, 0, len(blocks))
	txs := make([][]chaindb.Transaction, 0, len(blocks))
	for _, bd := range blocks {
		all = append(all, *bd.Block)
		txs = append(txs, bd.Txs)
	}
	log.Printf("[p2p] 重組：已拉取 %d 塊，開始分批重放（fork=%d，每段 500 塊）", len(all), fork)
	const seg = 500
	for start := fork + 1; start <= best.Height; start += seg {
		end := start + seg - 1
		if end > best.Height {
			end = best.Height
		}
		segBlocks := all[start-fork-1 : end-fork]
		segTxs := txs[start-fork-1 : end-fork]
		if err := n.host.ReorgChain(start-1, segBlocks, segTxs); err != nil {
			log.Printf("[p2p] 重組失敗（段 %d..%d，%d 塊）: %v", start, end, len(segBlocks), err)
			return
		}
		log.Printf("[p2p] 重組進度：段 %d..%d 完成（累計 %d 塊）", start, end, end)
	}
	log.Printf("[p2p] 重組完成：鏈頂 %d", best.Height)
}

// reorgFullChain 信任 best peer 的創世並整鏈重放（本機自產創世與主網不同源時）。
func (n *Network) reorgFullChain(best Peer) {
	log.Printf("[p2p] 整鏈替換開始：拉取 0..%d 全部區塊", best.Height)
	blocks, ok := n.fetchBlocksParallel(best, 0, best.Height)
	if !ok {
		log.Printf("[p2p] 整鏈替換失敗：區塊拉取失敗/缺塊（此輪放棄，等待下一缺口觸發）")
		return
	}
	all := make([]chaindb.Block, 0, len(blocks))
	txs := make([][]chaindb.Transaction, 0, len(blocks))
	for _, bd := range blocks {
		all = append(all, *bd.Block)
		txs = append(txs, bd.Txs)
	}
	log.Printf("[p2p] 整鏈替換：已拉取 %d 塊，開始分批重放（每段 500 塊）", len(all))
	const seg = 500
	for start := int64(0); start <= best.Height; start += seg {
		end := start + seg - 1
		if end > best.Height {
			end = best.Height
		}
		segBlocks := all[start : end+1]
		segTxs := txs[start : end+1]
		if err := n.host.ReorgChain(start-1, segBlocks, segTxs); err != nil {
			log.Printf("[p2p] 整鏈替換失敗（段 %d..%d，%d 塊）: %v", start, end, len(segBlocks), err)
			return
		}
		log.Printf("[p2p] 整鏈替換進度：段 %d..%d 完成（累計 %d 塊）", start, end, end+1)
	}
	log.Printf("[p2p] 整鏈替換完成：鏈頂 %d", best.Height)
}

func (n *Network) fetchPeerBlock(p Peer, h int64) (*chaindb.Block, []chaindb.Transaction, error) {
	if n.ctx == nil {
		return nil, nil, fmt.Errorf("p2p: not started")
	}
	url := p.BaseURL + "/p2p/block/" + strconv.FormatInt(h, 10)
	req, err := http.NewRequestWithContext(n.ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("fetch block %d status %d", h, resp.StatusCode)
	}
	var d blockData
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, nil, err
	}
	return d.Block, d.Txs, nil
}
