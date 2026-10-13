package node

import (
	"context"
	"fmt"
	"log"
	"net/http"

	bridgepkg "tacm/internal/bridge"
)

// BSCRelayState 為 BSC 中繼狀態（RPC 回應用）。
type BSCRelayState struct {
	Enabled     bool   `json:"enabled"`
	RPC         string `json:"rpc"`
	ChainID     int64  `json:"chain_id"`
	TokenAddr   string `json:"token_addr"`
	LockProxy   string `json:"lock_proxy"`
	SignerAddr  string `json:"signer_addr"`
	LastScanned int64  `json:"last_scanned"`
}

// StartBSCRelay 在本節點啟用 BSC 雙向中繼（需先掛載跨鏈橋）。
func (n *Node) StartBSCRelay(cfg bridgepkg.RelayerConfig) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.bridge == nil {
		return fmt.Errorf("BSC 中繼需先啟用跨鏈橋（-bridge）")
	}
	if n.bscRelay != nil {
		return fmt.Errorf("BSC 中繼已啟用")
	}
	r, err := bridgepkg.NewRelayer(cfg, n.bridge)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	n.bscRelay = r
	n.bscRelayCancel = cancel
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[bsc-relay] 中繼 goroutine panic: %v", rec)
			}
		}()
		r.Run(ctx)
	}()
	return nil
}

// BSCRelayStatus 回中繼狀態。
func (n *Node) BSCRelayStatus() BSCRelayState {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.bscRelay == nil {
		return BSCRelayState{}
	}
	st := BSCRelayState{
		Enabled:   true,
		RPC:       n.bscRelay.Config().BSCTestRPC,
		ChainID:   n.bscRelay.Config().BSCChainID,
		TokenAddr: n.bscRelay.Config().BSCTokenAddr,
		LockProxy: n.bscRelay.Config().BSCLockProxyAddr,
	}
	if s := n.bscRelay.Signer(); s != nil {
		st.SignerAddr = s.Address()
	}
	if v, err := n.bscRelay.LastScanned(); err == nil {
		st.LastScanned = v
	}
	return st
}

// stopBSCRelay 停止中繼（Close 時調用）。
func (n *Node) stopBSCRelay() {
	if n.bscRelay != nil && n.bscRelayCancel != nil {
		n.bscRelayCancel()
		n.bscRelayCancel = nil
		if err := n.bscRelay.Close(); err != nil {
			log.Printf("[bsc-relay] 關閉失敗: %v", err)
		}
		n.bscRelay = nil
	}
}

// handleBridgeBSCStatus GET /bridge/bsc/status。
func (s *RPCServer) handleBridgeBSCStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.node.BSCRelayStatus())
}
