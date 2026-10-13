package p2p

import (
	"strings"
	"time"
)

func normURL(u string) string { return strings.TrimRight(strings.TrimSpace(u), "/") }

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

	if oldID, ok := n.urlIndex[u]; ok && oldID != nodeID {
		delete(n.peers, oldID)
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
	n.urlIndex[u] = nodeID
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
		out = append(out, *p)
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
