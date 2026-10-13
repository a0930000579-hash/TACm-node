package node

import "tacm/internal/p2p"

// AttachP2P 創建並掛載 P2P 網絡。selfURL 為本節點對外可達基址
// （如 http://127.0.0.1:8333），bootstrap 為初始引導節點基址列表。
// 返回網絡實例，調用方負責 Start(ctx) 並把其 Handler 掛載到 HTTP 服務。
func (n *Node) AttachP2P(selfURL string, bootstrap []string) (*p2p.Network, error) {
	net, err := p2p.New(p2p.Config{
		SelfID:    n.nodeID,
		SelfURL:   selfURL,
		Bootstrap: bootstrap,
	}, n.P2PHost())
	if err != nil {
		return nil, err
	}
	n.p2pNet = net
	return net, nil
}

// P2PNet 返回已掛載的 P2P 網絡（未聯網時為 nil）。
func (n *Node) P2PNet() *p2p.Network { return n.p2pNet }
