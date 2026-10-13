package p2p

import (
	"context"
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
	peers      map[string]*Peer  // nodeID（或臨時 peer@url）-> Peer
	urlIndex   map[string]string // baseURL -> nodeID
	seenBlocks map[string]struct{}
	seenTxs    map[string]struct{}
	seenVotes  map[string]struct{}
	seenViewCh map[string]struct{}
	seenBridge map[string]struct{}

	wg     sync.WaitGroup
	ctx    context.Context
	cancel context.CancelFunc
}

// New 構造 P2P 網絡並加入引導節點（尚未啟動循環）。
func New(cfg Config, host Host) (*Network, error) {
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = 2 * time.Second
	}
	if cfg.SyncInterval <= 0 {
		cfg.SyncInterval = 2 * time.Second
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 2 * time.Second
	}
	if cfg.MaxPeers <= 0 {
		cfg.MaxPeers = 50
	}
	n := &Network{
		cfg:        cfg,
		host:       host,
		client:     &http.Client{Timeout: cfg.DialTimeout},
		mux:        http.NewServeMux(),
		peers:      make(map[string]*Peer),
		urlIndex:   make(map[string]string),
		seenBlocks: make(map[string]struct{}),
		seenTxs:    make(map[string]struct{}),
		seenVotes:  make(map[string]struct{}),
		seenViewCh: make(map[string]struct{}),
		seenBridge: make(map[string]struct{}),
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
