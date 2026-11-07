package p2p

import (
	"strings"
	"time"
)

func normURL(u string) string { return strings.TrimRight(strings.TrimSpace(u), "/") }

// filterDialable 過濾出有可達 BaseURL 的節點——外連節點（無自身 URL）無法被
// 連入，所有主動撥號（心跳/廣播/刷新/拉塊）都應跳過，僅靠其入站請求保持在線。
func filterDialable(ps []Peer) []Peer {
	out := make([]Peer, 0, len(ps))
	for _, p := range ps {
		if normURL(p.BaseURL) != "" {
			out = append(out, p)
		}
	}
	return out
}

// AddPeerByURL 手動加入 P2P 對等節點（admin_addPeer 用）；自身、重複或已滿返回 false。
func (n *Network) AddPeerByURL(rawURL string) bool {
	if n == nil {
		return false
	}
	u := normURL(rawURL)
	if u == "" {
		return false
	}
	n.mu.Lock()
	_, exists := n.urlIndex[u]
	full := len(n.peers) >= n.cfg.MaxPeers
	self := u == normURL(n.cfg.SelfURL)
	n.mu.Unlock()
	if exists || full || self {
		return false
	}
	n.addPeerByURL(rawURL)
	return true
}

// addPeerByURL 以臨時 id 加入尚未握手的節點；自身、重複或已滿則忽略。
func (n *Network) addPeerByURL(rawURL string) {
	u := normURL(rawURL)
	if u == "" || u == normURL(n.cfg.SelfURL) {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.peers) >= n.cfg.MaxPeers {
		return
	}
	if _, exists := n.urlIndex[u]; exists {
		return
	}
	id := "peer@" + u
	n.peers[id] = &Peer{NodeID: id, BaseURL: u}
	n.urlIndex[u] = id
}

// registerPeer 以握手信息登記/更新節點，臨時 id 轉正。
func (n *Network) registerPeer(nodeID, rawURL, owner string, height, finalized int64) *Peer {
	u := normURL(rawURL)
	n.mu.Lock()
	defer n.mu.Unlock()

	// 外連節點無可達 URL：僅以 NodeID 索引，不進 urlIndex（避免空字串互相覆蓋）。
	if u != "" {
		if oldID, ok := n.urlIndex[u]; ok && oldID != nodeID {
			delete(n.peers, oldID)
		}
	}
	p, exists := n.peers[nodeID]
	if !exists {
		p = &Peer{NodeID: nodeID, BaseURL: u}
		n.peers[nodeID] = p
	}
	p.BaseURL = u
	if owner != "" {
		p.Owner = owner
	}
	p.Height = height
	p.Finalized = finalized
	p.Online = true
	p.LastSeen = time.Now()
	p.failCount = 0
	if u != "" {
		n.urlIndex[u] = nodeID
	}
	return p
}

func (n *Network) markFail(p *Peer) {
	n.mu.Lock()
	defer n.mu.Unlock()
	p.failCount++
	if p.failCount >= 3 {
		p.Online = false
	}
}

// snapshotPeers 返回節點信息副本（併發安全）。
// PeerSnapshot M86：公開 peers 快照（節點獨立 UI / 運維面板用）。
func (n *Network) PeerSnapshot() []Peer { return n.snapshotPeers() }

func (n *Network) snapshotPeers() []Peer {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]Peer, 0, len(n.peers))
	for _, p := range n.peers {
		cp := *p
		// v65：補上每個 peer 的通道加密狀態（sealBody 自動降級時為 false）。
		cp.Encrypted = n.peerEncrypted(p.BaseURL)
		out = append(out, cp)
	}
	return out
}

// onlinePeers 返回在線節點的值快照（併發安全；調用方讀自己的副本，
// 不與 registerPeer/markFail 的併發寫衝突）。
func (n *Network) onlinePeers() []Peer {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]Peer, 0, len(n.peers))
	for _, p := range n.peers {
		if p.Online {
			out = append(out, *p)
		}
	}
	return out
}

func (n *Network) peerURLs() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]string, 0, len(n.peers))
	for _, p := range n.peers {
		out = append(out, p.BaseURL)
	}
	return out
}
