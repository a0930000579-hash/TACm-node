package p2p

import "time"

// reorgBackoffEntry 記錄某 peer 最近一次被拒絕的重組嘗試。
type reorgBackoffEntry struct {
	at         time.Time // 拒絕時刻
	peerHeight int64     // 拒絕時該 peer 的鏈頂
}

const (
	// reorgCooldown：同一 peer 重組被拒後的最小冷卻時間，期間不重拉整段分叉。
	reorgCooldown = 60 * time.Second
	// reorgLeadMargin：peer 領先幅度較拒絕時再拉大這麼多塊，才提前解除冷卻
	// （代表對方鏈真的更重、應採納），避免等難度賽跑的活鎖。
	reorgLeadMargin = 5
)

// reorgSuppressed 判斷是否應跳過對該 peer 的重組：仍在冷卻期內，且 peer 領先
// 幅度未較上次拒絕時顯著拉大。
func (n *Network) reorgSuppressed(p Peer, self int64) bool {
	n.mu.Lock()
	e, ok := n.reorgBackoff[p.NodeID]
	n.mu.Unlock()
	if !ok {
		return false
	}
	if time.Since(e.at) >= reorgCooldown {
		return false
	}
	lead := p.Height - self
	leadAtReject := e.peerHeight - self
	return lead < leadAtReject+reorgLeadMargin
}

// noteReorgRejected 記錄一次重組拒絕（重拉/重放失敗或累積難度不更重）。
func (n *Network) noteReorgRejected(p Peer) {
	n.mu.Lock()
	n.reorgBackoff[p.NodeID] = reorgBackoffEntry{at: time.Now(), peerHeight: p.Height}
	n.mu.Unlock()
}
