package node

import (
	"context"
	"fmt"
	"log"
	"net/http"

	bridgepkg "tacm/internal/bridge"
)

// BSCRelayState 為 BSC 中繼狀態（RPC 回應用）。
// M150h：多鏈擴展——ChainName 標示來源鏈（bsc/ethereum/polygon…）。
type BSCRelayState struct {
	Enabled     bool   `json:"enabled"`
	ChainName   string `json:"chain_name"`
	RPC         string `json:"rpc"`
	ChainID     int64  `json:"chain_id"`
	TokenAddr   string `json:"token_addr"`
	LockProxy   string `json:"lock_proxy"`
	SignerAddr  string `json:"signer_addr"`
	LastScanned int64  `json:"last_scanned"`
	// M149r：多資產互通——中繼器當前啟用的跨鏈資產清單。
	Assets []bridgepkg.AssetConfig `json:"assets"`
}

// StartChainRelay 在本節點啟用指定鏈（name：bsc/ethereum/polygon…）的雙向中繼
// （需先掛載跨鏈橋）。M150h：多鏈可並存——每鏈一實例，config key/水位互相獨立。
func (n *Node) StartChainRelay(name string, cfg bridgepkg.RelayerConfig) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if name == "" {
		name = "bsc"
	}
	if n.bridge == nil {
		return fmt.Errorf("%s 中繼需先啟用跨鏈橋（-bridge）", name)
	}
	if n.relays == nil {
		n.relays = make(map[string]*bridgepkg.Relayer)
		n.relayCancels = make(map[string]context.CancelFunc)
	}
	if n.relays[name] != nil {
		return fmt.Errorf("%s 中繼已啟用", name)
	}
	r, err := bridgepkg.NewRelayer(cfg, n.bridge)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	n.relays[name] = r
	n.relayCancels[name] = cancel
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[%s-relay] 中繼 goroutine panic: %v", name, rec)
			}
		}()
		r.Run(ctx)
	}()
	return nil
}

// StartBSCRelay 在本節點啟用 BSC 雙向中繼（M150h：向後相容 wrapper）。
func (n *Node) StartBSCRelay(cfg bridgepkg.RelayerConfig) error {
	return n.StartChainRelay("bsc", cfg)
}

// BSCRelayStatus 回 BSC 中繼狀態（M150j：向後相容 wrapper，等價 RelayStatus("bsc")）。
func (n *Node) BSCRelayStatus() BSCRelayState {
	return n.RelayStatus("bsc")
}

// RelayStatus 回指定鏈中繼狀態（M150j：name 空則預設 bsc；未啟用回零值 state）。
func (n *Node) RelayStatus(name string) BSCRelayState {
	if name == "" {
		name = "bsc"
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	r := n.relays[name]
	if r == nil {
		return BSCRelayState{}
	}
	cfg := r.Config()
	st := BSCRelayState{
		Enabled:   true,
		ChainName: cfg.ChainName,
		RPC:       cfg.BSCTestRPC,
		ChainID:   cfg.BSCChainID,
		TokenAddr: cfg.BSCTokenAddr,
		LockProxy: cfg.BSCLockProxyAddr,
	}
	if s := r.Signer(); s != nil {
		st.SignerAddr = s.Address()
	}
	if v, err := r.LastScanned(); err == nil {
		st.LastScanned = v
	}
	st.Assets = append([]bridgepkg.AssetConfig(nil), cfg.Assets...)
	return st
}

// stopChainRelay 停止指定鏈中繼（Close 時調用）。
func (n *Node) stopChainRelay(name string) {
	if n.relays == nil {
		return
	}
	r := n.relays[name]
	if r == nil {
		return
	}
	if c := n.relayCancels[name]; c != nil {
		c()
		delete(n.relayCancels, name)
	}
	if err := r.Close(); err != nil {
		log.Printf("[%s-relay] 關閉失敗: %v", name, err)
	}
	delete(n.relays, name)
}

// stopBSCRelay 停止 BSC 中繼（M150h：向後相容 wrapper）。
func (n *Node) stopBSCRelay() {
	n.stopChainRelay("bsc")
}

// stopAllRelays 停止全部多鏈中繼（Close 時調用；M150h：多鏈並存）。
func (n *Node) stopAllRelays() {
	if n.relays == nil {
		return
	}
	for name := range n.relays {
		n.stopChainRelay(name)
	}
}

// handleBridgeBSCStatus GET /bridge/bsc/status?chain=bsc|ethereum|polygon。
// chain 空則預設 bsc；回該鏈 relay 狀態（未啟用 → enabled=false 零值 state）。
func (s *RPCServer) handleBridgeBSCStatus(w http.ResponseWriter, r *http.Request) {
	chain := r.URL.Query().Get("chain")
	writeJSON(w, http.StatusOK, s.node.RelayStatus(chain))
}
