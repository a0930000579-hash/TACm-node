package node

import "tacm/internal/p2p"

// AttachP2P 創建並掛載 P2P 網絡。selfURL 為本節點對外可達基址
// （如 http://127.0.0.1:8333），bootstrap 為初始引導節點基址列表。
// encrypt=true 啟用 P2P 訊息層加密（ECDH+AES-GCM，需全網同時開啟）。
// 返回網絡實例，調用方負責 Start(ctx) 並把其 Handler 掛載到 HTTP 服務。
func (n *Node) AttachP2P(selfURL string, bootstrap []string, encrypt bool) (*p2p.Network, error) {
	net, err := p2p.New(p2p.Config{
		SelfID:    n.nodeID,
		SelfURL:   selfURL,
		Bootstrap: bootstrap,
		Encrypt:   encrypt,
		// v54：Owner 帶上本節點鏈上地址——peer 心跳/握手以此廣播 Owner，
		// 失聯驗證人接管判定才能區分「在線 peer 是否為共識驗證人」
		// （僅本節點為唯一在線驗證人時才接管，避免多驗證人網路中
		// 接管與 view-change 同時出塊造成分叉）。
		Owner: n.nodeAddress,
	}, n.P2PHost())
	if err != nil {
		return nil, err
	}
	n.p2pNet = net
	return net, nil
}

// P2PNet 返回已掛載的 P2P 網絡（未聯網時為 nil）。
func (n *Node) P2PNet() *p2p.Network { return n.p2pNet }
