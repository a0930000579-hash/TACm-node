package p2p

import (
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
			// 退避調節：有進展→回到基準；無進展（連不上/被防護）→ 翻倍至 60s 上限。
			if progress {
				interval = base
			} else if interval < 60*time.Second {
				interval *= 2
				if interval > 60*time.Second {
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

// syncOnce 嘗試追齊；回傳 true 表示本輪有實際進展（任一 peer 完成拉取）。
func (n *Network) syncOnce() bool {
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
		pb, _, perr := n.fetchPeerBlock(p, self)
		lb, _, lerr := n.host.BlockDetail(self)
		if perr != nil || pb == nil || lerr != nil || lb == nil {
			return false
		}
		if pb.Hash != lb.Hash {
			n.doReorg(p, self)
			return true
		}
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
			sb, stxs, serr := n.fetchPeerBlock(p, bd.Block.Height)
			if serr != nil || sb == nil {
				return false
			}
			if err := n.host.OnIncomingBlock(sb, stxs); err != nil {
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
	for range segs {
		r := <-results
		if r.err != nil {
			return nil, false
		}
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
		pb, _, perr := n.fetchPeerBlock(best, h)
		lb, _, lerr := n.host.BlockDetail(h)
		if perr != nil || pb == nil || lerr != nil || lb == nil {
			continue
		}
		if pb.Hash == lb.Hash {
			fork = h
			break
		}
	}
	if fork < 0 {
		return // 創世都不一致，拒絕重組
	}

	blocks := make([]chaindb.Block, 0, best.Height-fork)
	allTxs := make([][]chaindb.Transaction, 0, best.Height-fork)
	for h := fork + 1; h <= best.Height; h++ {
		b, txs, err := n.fetchPeerBlock(best, h)
		if err != nil || b == nil {
			return
		}
		blocks = append(blocks, *b)
		allTxs = append(allTxs, txs)
	}
	_ = n.host.ReorgChain(fork, blocks, allTxs)
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
