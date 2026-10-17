package node

import (
	"encoding/json"
	"fmt"
	"net/http"

	bridgepkg "tacm/internal/bridge"
)

type bridgeTransferReq struct {
	SourceChain   string  `json:"source_chain"`
	TargetChain   string  `json:"target_chain"`
	SourceAddress string  `json:"source_address"`
	TargetAddress string  `json:"target_address"`
	Amount        float64 `json:"amount"`
	Token         string  `json:"token"`
	SourceTxHash  string  `json:"source_tx_hash"` // 源鏈鎖定/銷毀證明哈希（網絡化必填）
}

type bridgeIDReq struct {
	BridgeTxID string `json:"bridge_tx_id"`
	TxHash     string `json:"tx_hash"`
}

func decodeBridgeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func (s *RPCServer) requireBridge(w http.ResponseWriter) *bridgepkg.Bridge {
	if s.node.bridge == nil {
		writeErr(w, http.StatusServiceUnavailable,
			"bridge not enabled; start node with -bridge")
		return nil
	}
	return s.node.bridge
}

func (s *RPCServer) handleBridgeChains(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK,
		map[string]any{"chains": bridgepkg.SupportedChains()})
}

// startNetworkedTransfer 走守衛網絡：建本地記錄 → 廣播提案 → 本節點即時簽名。
func (s *RPCServer) startNetworkedTransfer(direction string, req bridgeTransferReq) (any, error) {
	if req.SourceTxHash == "" {
		return nil, fmt.Errorf("networked bridge 需要 source_tx_hash（源鏈鎖定/銷毀證明）")
	}
	m, res, err := s.node.bridge.NetworkedTransfer(direction,
		req.SourceChain, req.TargetChain,
		req.SourceAddress, req.TargetAddress,
		req.Amount, req.Token, req.SourceTxHash, s.node.Address())
	if err != nil {
		return nil, err
	}
	if s.node.p2pNet != nil {
		s.node.p2pNet.BroadcastBridgeProposal(m)
	}
	if sig, err := s.node.bridge.HandleProposal(m); err == nil && sig != nil && s.node.p2pNet != nil {
		s.node.p2pNet.BroadcastBridgeSig(m.MessageID, sig, m)
	}
	return res, nil
}

func (s *RPCServer) handleBridgeLock(w http.ResponseWriter, r *http.Request) {
	b := s.requireBridge(w)
	if b == nil {
		return
	}
	var req bridgeTransferReq
	if !decodeBridgeBody(w, r, &req) {
		return
	}
	if s.node.bridgeNetworked {
		res, err := s.startNetworkedTransfer("lock", req)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, res)
		return
	}
	res, err := b.LockAndMint(req.SourceChain, req.TargetChain,
		req.SourceAddress, req.TargetAddress, req.Amount, req.Token)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *RPCServer) handleBridgeBurn(w http.ResponseWriter, r *http.Request) {
	b := s.requireBridge(w)
	if b == nil {
		return
	}
	var req bridgeTransferReq
	if !decodeBridgeBody(w, r, &req) {
		return
	}
	if s.node.bridgeNetworked {
		res, err := s.startNetworkedTransfer("burn", req)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, res)
		return
	}
	res, err := b.BurnAndUnlock(req.SourceChain, req.TargetChain,
		req.SourceAddress, req.TargetAddress, req.Amount, req.Token)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *RPCServer) handleBridgeConfirmLock(w http.ResponseWriter, r *http.Request) {
	b := s.requireBridge(w)
	if b == nil {
		return
	}
	var req bridgeIDReq
	if !decodeBridgeBody(w, r, &req) {
		return
	}
	tx, err := b.ConfirmLock(req.BridgeTxID, req.TxHash)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tx)
}

func (s *RPCServer) handleBridgeMint(w http.ResponseWriter, r *http.Request) {
	b := s.requireBridge(w)
	if b == nil {
		return
	}
	var req bridgeIDReq
	if !decodeBridgeBody(w, r, &req) {
		return
	}
	tx, err := b.MintOnTarget(req.BridgeTxID, req.TxHash)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tx)
}

func (s *RPCServer) handleBridgeConfirmBurn(w http.ResponseWriter, r *http.Request) {
	b := s.requireBridge(w)
	if b == nil {
		return
	}
	var req bridgeIDReq
	if !decodeBridgeBody(w, r, &req) {
		return
	}
	tx, err := b.ConfirmBurn(req.BridgeTxID, req.TxHash)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tx)
}

func (s *RPCServer) handleBridgeUnlock(w http.ResponseWriter, r *http.Request) {
	b := s.requireBridge(w)
	if b == nil {
		return
	}
	var req bridgeIDReq
	if !decodeBridgeBody(w, r, &req) {
		return
	}
	tx, err := b.UnlockOnTarget(req.BridgeTxID, req.TxHash)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tx)
}

func (s *RPCServer) handleBridgeStatus(w http.ResponseWriter, r *http.Request) {
	b := s.requireBridge(w)
	if b == nil {
		return
	}
	var req bridgeIDReq
	if !decodeBridgeBody(w, r, &req) {
		return
	}
	tx, err := b.Status(req.BridgeTxID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tx)
}

func (s *RPCServer) handleBridgeStats(w http.ResponseWriter, r *http.Request) {
	b := s.requireBridge(w)
	if b == nil {
		return
	}
	stats, err := b.Stats()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// requireBridgeAdmin 檢查管理員身份：橋管理端點要求登入（kill-switch 不能匿名觸發）。
func (s *RPCServer) requireBridgeAdmin(w http.ResponseWriter, r *http.Request) bool {
	auth := s.node.Auth()
	if auth == nil {
		writeErr(w, http.StatusServiceUnavailable, "auth service not enabled")
		return false
	}
	u, err := auth.CurrentUser(r)
	if err != nil || u == nil {
		writeErr(w, http.StatusUnauthorized, "login required: bridge kill-switch is admin-only")
		return false
	}
	return true
}

// handleBridgePause POST /bridge/pause — 緊急暫停（kill-switch）：拒絕所有橋操作。
func (s *RPCServer) handleBridgePause(w http.ResponseWriter, r *http.Request) {
	b := s.requireBridge(w)
	if b == nil {
		return
	}
	if !s.requireBridgeAdmin(w, r) {
		return
	}
	if err := b.SetPaused(true); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "paused": true})
}

// handleBridgeResume POST /bridge/resume — 恢復橋操作。
func (s *RPCServer) handleBridgeResume(w http.ResponseWriter, r *http.Request) {
	b := s.requireBridge(w)
	if b == nil {
		return
	}
	if !s.requireBridgeAdmin(w, r) {
		return
	}
	if err := b.SetPaused(false); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "paused": false})
}

// handleBridgePauseStatus GET /bridge/pause_status — 回報 kill-switch 狀態（供前端/監控輪詢）。
func (s *RPCServer) handleBridgePauseStatus(w http.ResponseWriter, r *http.Request) {
	b := s.requireBridge(w)
	if b == nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"paused": b.IsPaused()})
}
