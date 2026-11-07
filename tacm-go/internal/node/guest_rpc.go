package node

// guest_rpc.go — M37/M38：訪客獨立地址與私鑰備份/恢復。
// 訪客不再共用節點地址；首次訪問由伺服器生成一個獨立 tx0 地址（隨機 secp256k1 金鑰對），
// 並回傳 WIF 私鑰供前端本地備份（換裝置可透過 WIF 恢復同一地址，資產不丟失）。
// 離鏈轉帳為帳本授權操作（無需私鑰簽名），私鑰僅用於地址派生與恢復驗證。

import (
	"encoding/json"
	"net/http"

	"tacm/internal/crypto"
)

// handleGuestAddress GET /api/guest/address — 為訪客生成獨立 tx0 地址（附 WIF 私鑰，供前端備份）。
func (s *RPCServer) handleGuestAddress(w http.ResponseWriter, r *http.Request) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "訪客地址生成失敗: "+err.Error())
		return
	}
	addr, err := kp.Address()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "訪客地址派生失敗: "+err.Error())
		return
	}
	wif, err := kp.WIF()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "訪客私鑰匯出失敗: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "address": addr, "wif": wif,
	})
}

// handleGuestRestore POST /api/guest/restore — 以 WIF 私鑰恢復訪客地址（換裝置不丟資產）。
func (s *RPCServer) handleGuestRestore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WIF string `json:"wif"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "請求格式錯誤: "+err.Error())
		return
	}
	if req.WIF == "" {
		writeErr(w, http.StatusBadRequest, "缺少 WIF 私鑰")
		return
	}
	kp, err := crypto.KeyPairFromWIF(req.WIF)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "WIF 無效，無法恢復地址")
		return
	}
	addr, err := kp.Address()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "訪客地址派生失敗: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "address": addr,
	})
}
