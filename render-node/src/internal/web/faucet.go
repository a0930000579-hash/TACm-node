package web

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/flosch/pongo2/v6"

	"tacm/internal/crypto"
)

// faucetDailyLimit 為單地址每日領取額度（TACm），與 node.FaucetClaimAmount 一致。
const faucetDailyLimit = "1000"

// faucetQuota 追蹤每日領取（地址 → 上次領取日期，YYYY-MM-DD）。
type faucetQuota struct {
	mu    sync.Mutex
	last  map[string]string
	limit string
}

func newFaucetQuota() *faucetQuota {
	return &faucetQuota{last: make(map[string]string), limit: faucetDailyLimit}
}

// claimedToday 檢查並記錄領取（同一地址當日僅一次）。
func (f *faucetQuota) claimedToday(addr string) bool {
	today := time.Now().Format("2006-01-02")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.last[addr] == today {
		return true
	}
	f.last[addr] = today
	return false
}

// handleFaucet GET /faucet — 水龍頭頁（開發者測試幣）。
func (s *Server) handleFaucet(w http.ResponseWriter, r *http.Request) {
	s.render(w, "faucet.html", pongo2.Context{
		"limit": s.faucet.limit,
		"node":  s.ds.Status(),
	})
}

// handleFaucetClaimAPI POST /api/faucet/claim — 為指定地址發放測試幣（每日限額）。
func (s *Server) handleFaucetClaimAPI(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Address string `json:"address"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrJSON(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	if !crypto.IsValidAddress(req.Address) {
		writeErrJSON(w, http.StatusBadRequest, "invalid address")
		return
	}
	if s.faucet.claimedToday(req.Address) {
		writeErrJSON(w, http.StatusTooManyRequests, "daily limit reached for this address")
		return
	}
	txHash, from, err := s.ds.FaucetClaim(req.Address, s.faucet.limit)
	if err != nil {
		writeErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"ok": true, "tx_hash": txHash, "status": "mempool",
		"from": from, "to": req.Address, "amount": s.faucet.limit,
	})
}

// writeErrJSON 寫出 JSON 錯誤回應（{ok:false,error:msg}）。
func writeErrJSON(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": msg})
}
