package node

import (
	"net/http"
)

// ===== Web3 錢包簽名登入（EIP-4361 SIWE）RPC =====
// 路由（由整合者於 rpc.go 註冊）：
//   POST /api/auth/web3/nonce  → handleWeb3Nonce
//   POST /api/auth/web3/verify → handleWeb3Verify

// handleWeb3Nonce 取得一次性登入挑戰訊息。
// body: { "address": "0x…40", "chain_id": "1"(選填) }
// 回應: { "ok": true, "nonce": "…", "message": "<完整 EIP-4361 訊息>" }
func (s *RPCServer) handleWeb3Nonce(w http.ResponseWriter, r *http.Request) {
	if s.node.authSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "auth 未啟用")
		return
	}
	var p struct {
		Address string `json:"address"`
		ChainID string `json:"chain_id"`
	}
	if err := decodeJSON(r, &p); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "message": "參數格式錯誤"})
		return
	}
	message, nonce, err := s.node.authSvc.NewWeb3Challenge(p.Address, r.Host, p.ChainID)
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "nonce": nonce, "message": message})
}

// handleWeb3Verify 驗證錢包 personal_sign 簽名，成功即建立 session（設定 cookie）。
// body: { "address": "0x…40", "message": "<nonce 端點回傳的訊息>", "signature": "0x…130 hex" }
// 回應: { "ok": true, "token": "…", "user": AuthUser, "uid": N }
func (s *RPCServer) handleWeb3Verify(w http.ResponseWriter, r *http.Request) {
	if s.node.authSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "auth 未啟用")
		return
	}
	var p struct {
		Address   string `json:"address"`
		Message   string `json:"message"`
		Signature string `json:"signature"`
	}
	if err := decodeJSON(r, &p); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "message": "參數格式錯誤"})
		return
	}
	uid, token, user, err := s.node.authSvc.VerifyWeb3Signature(p.Address, p.Message, p.Signature)
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	SetAuthCookie(w, token)
	writeJSON(w, 200, map[string]any{"ok": true, "token": token, "user": user, "uid": uid})
}
