package p2p

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"
)

// Network 為一個節點的 P2P 網絡實例。
type Network struct {
	cfg    Config
	host   Host
	client *http.Client
	mux    *http.ServeMux

	mu         sync.Mutex
	syncMu     sync.Mutex // M149d：同步在跑鎖（防缺口觸發的 syncOnce 疊加 → OOM 重啟循環）
	peers      map[string]*Peer  // nodeID（或臨時 peer@url）-> Peer
	urlIndex   map[string]string // baseURL -> nodeID
	seenBlocks map[string]struct{}
	seenTxs    map[string]struct{}
	seenVotes  map[string]struct{}
	seenViewCh map[string]struct{}
	seenBridge map[string]struct{}

	// M162：P2P 訊息層加密（cfg.Encrypt=true 時啟用）。
	Secure      *Secure
	peerPubKeys sync.Map // baseURL -> peer pubkey hex（加密握手緩存）
	// v65：peerCipher 記錄各 peer（baseURL -> 是否加密通道）。
	// 對端不支援加密時 sealBody 自動降級明文並於此標記 false，供
	// Peer.Encrypted 與節點 UI 展示（避免加密端誤判對端狀態）。
	peerCipher sync.Map // baseURL -> bool

	// reorgBackoff：對等難度分叉的重組抑制。兩條等難度鏈賽跑時，sync 每個 tick
	// 都重拉整段分叉並在驗證時以「累積難度不更重」拒絕，形成 CPU/網路熱迴圈。
	// 記住各 peer 最近一次被拒的重組，在冷卻期或領先幅度未顯著拉大時跳過重拉。
	reorgBackoff map[string]reorgBackoffEntry

	wg     sync.WaitGroup
	ctx    context.Context
	cancel context.CancelFunc
}

// New 構造 P2P 網絡並加入引導節點（尚未啟動循環）。
func New(cfg Config, host Host) (*Network, error) {
	if cfg.HeartbeatInterval <= 0 {
		// 15s 保活——「雲端防護封鎖資料中心 IP」為誤判（實為沙箱代理），恢復原預設；
		// 心跳越快，peer 高度資訊越新，追高越接近秒級。
		cfg.HeartbeatInterval = 15 * time.Second
	}
	if cfg.SyncInterval <= 0 {
		cfg.SyncInterval = 2 * time.Second
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 2 * time.Second
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 60 * time.Second
	}
	if cfg.MaxPeers <= 0 {
		cfg.MaxPeers = 50
	}
	n := &Network{
		cfg:        cfg,
		host:       host,
		client: &http.Client{
			Timeout: cfg.RequestTimeout, // 整體請求上限（大 JSON 區塊拉取）
			Transport: &http.Transport{
				// 撥號超時維持快速失敗；讀 body 由 RequestTimeout 控制。
				DialContext: (&net.Dialer{Timeout: cfg.DialTimeout}).DialContext,
			},
		},
		mux:        http.NewServeMux(),
		peers:      make(map[string]*Peer),
		urlIndex:   make(map[string]string),
		seenBlocks: make(map[string]struct{}),
		seenTxs:    make(map[string]struct{}),
		seenVotes:  make(map[string]struct{}),
		seenViewCh: make(map[string]struct{}),
		seenBridge: make(map[string]struct{}),
		reorgBackoff: make(map[string]reorgBackoffEntry),
	}
	if cfg.Encrypt {
		sec, err := NewSecure()
		if err != nil {
			return nil, err
		}
		n.Secure = sec
	}
	for _, url := range cfg.Bootstrap {
		n.addPeerByURL(url)
	}
	n.registerRoutes()
	return n, nil
}

func (n *Network) registerRoutes() {
	n.mux.HandleFunc("POST /p2p/hello", n.handleHello)
	n.mux.HandleFunc("POST /p2p/gossip", n.handleGossip)
	n.mux.HandleFunc("GET /p2p/pubkey", n.handlePubKey) // M162：加密握手用
	n.mux.HandleFunc("GET /p2p/block/{height}", n.handleBlockFetch)
	n.mux.HandleFunc("GET /p2p/blocks", n.handleBlocksFetch) // M86：批量同步（catch-up）
	n.mux.HandleFunc("GET /p2p/peers", n.handlePeers)
}

// Handler 返回可掛載到節點 HTTP 服務的 P2P 處理器（含 CORS）。
func (n *Network) Handler() http.Handler { return cors(n.mux) }

// Start 啟動心跳、同步後台循環；重複調用安全。
func (n *Network) Start(ctx context.Context) {
	ctx, n.cancel = context.WithCancel(ctx)
	n.ctx = ctx
	n.wg.Add(2)
	go n.heartbeatLoop(ctx)
	go n.syncLoop(ctx)
}

// Close 停止後台循環。
func (n *Network) Close() {
	if n.cancel != nil {
		n.cancel()
	}
	n.wg.Wait()
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}
