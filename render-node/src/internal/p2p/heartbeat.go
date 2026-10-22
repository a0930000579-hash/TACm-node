package p2p

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

func (n *Network) heartbeatLoop(ctx context.Context) {
	defer n.wg.Done()
	n.heartbeatOnce() // 立即握手一次
	ticker := time.NewTicker(n.cfg.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n.heartbeatOnce()
		}
	}
}

func (n *Network) heartbeatOnce() {
	req := helloReq{
		Version: ProtocolVersion, NodeID: n.cfg.SelfID, BaseURL: n.cfg.SelfURL,
		Owner: n.cfg.Owner, Height: n.host.Height(),
		Finalized: n.host.FinalizedHeight(), Peers: n.peerURLs(),
	}
	body, err := json.Marshal(req)
	if err != nil {
		return
	}
	var wg sync.WaitGroup
	for _, p := range n.snapshotPeers() {
		wg.Add(1)
		go func(p Peer) {
			defer wg.Done()
			respBody, err := n.postHello(p.BaseURL+"/p2p/hello", body)
			if err != nil {
				n.markFailByURL(p.BaseURL)
				return
			}
			var resp helloResp
			if json.Unmarshal(respBody, &resp) != nil || !resp.OK {
				n.markFailByURL(p.BaseURL)
				return
			}
			n.registerPeer(resp.NodeID, resp.BaseURL, resp.Owner, resp.Height, resp.Finalized)
			for _, u := range resp.Peers {
				n.addPeerByURL(u)
			}
		}(p)
	}
	wg.Wait()
}

// RefreshHeights 向在線 peer 主動刷新高度快照（v54：恢復節點追平判定用，
// node 層 isBehindNetwork 在快照無領先者時呼叫，避免心跳 15s 滯後誤判）。
func (n *Network) RefreshHeights() {
	n.refreshPeerHeights()
}

// refreshPeerHeights v54：向在線 peer 主動刷新高度快照（同步等待）。
// sync tick 在「無進展」時調用——心跳 15s 滯後會讓恢復節點誤判已追平，
// 主動刷新後可把「主鏈恢復→追平」窗口縮短到單個 tick。
// 只打在線 peer（失敗已標 offline），配合無進展退避 60s 維持 M149 雲防護節奏。
func (n *Network) refreshPeerHeights() {
	req := helloReq{
		Version: ProtocolVersion, NodeID: n.cfg.SelfID, BaseURL: n.cfg.SelfURL,
		Owner: n.cfg.Owner, Height: n.host.Height(),
		Finalized: n.host.FinalizedHeight(), Peers: n.peerURLs(),
	}
	body, err := json.Marshal(req)
	if err != nil {
		return
	}
	var wg sync.WaitGroup
	for _, p := range n.onlinePeers() {
		wg.Add(1)
		go func(p Peer) {
			defer wg.Done()
			respBody, err := n.postHello(p.BaseURL+"/p2p/hello", body)
			if err != nil {
				n.markFailByURL(p.BaseURL)
				return
			}
			var resp helloResp
			if json.Unmarshal(respBody, &resp) != nil || !resp.OK {
				n.markFailByURL(p.BaseURL)
				return
			}
			n.registerPeer(resp.NodeID, resp.BaseURL, resp.Owner, resp.Height, resp.Finalized)
		}(p)
	}
	wg.Wait()
}

func (n *Network) postHello(url string, body []byte) ([]byte, error) {
	if n.ctx == nil {
		return nil, errors.New("p2p: network not started")
	}
	sealed, err := n.sealBody(url, body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(n.ctx, http.MethodPost, url, bytes.NewReader(sealed))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hello status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func (n *Network) markFailByURL(rawURL string) {
	u := normURL(rawURL)
	n.mu.Lock()
	defer n.mu.Unlock()
	id, ok := n.urlIndex[u]
	if !ok {
		return
	}
	p := n.peers[id]
	p.failCount++
	if p.failCount >= 3 {
		p.Online = false
	}
}
