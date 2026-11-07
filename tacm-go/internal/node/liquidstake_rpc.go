package node

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// liquidstakeDepositReq POST /api/liquidstake/deposit {address, amount}
type liquidstakeDepositReq struct {
	Address string  `json:"address"`
	Amount  float64 `json:"amount"`
}

// liquidstakeRedeemReq POST /api/liquidstake/redeem {address, amount}
type liquidstakeRedeemReq struct {
	Address string  `json:"address"`
	Amount  float64 `json:"amount"` // stTACm 憑證數
}

// handleLiquidstakeDeposit 存入 TACm，鑄造 stTACm 憑證。
func (s *RPCServer) handleLiquidstakeDeposit(w http.ResponseWriter, r *http.Request) {
	if s.node.ls == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "liquidstake_disabled"})
		return
	}
	var req liquidstakeDepositReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_request"})
		return
	}
	if req.Address == "" || req.Amount <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "address_and_amount_required"})
		return
	}
	minted, err := s.node.ls.Deposit(req.Address, req.Amount)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "minted_stTACm": minted})
}

// handleLiquidstakeRedeem 以 stTACm 贖回 TACm。
func (s *RPCServer) handleLiquidstakeRedeem(w http.ResponseWriter, r *http.Request) {
	if s.node.ls == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "liquidstake_disabled"})
		return
	}
	var req liquidstakeRedeemReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_request"})
		return
	}
	if req.Address == "" || req.Amount <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "address_and_amount_required"})
		return
	}
	out, err := s.node.ls.Redeem(req.Address, req.Amount)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tacm": out})
}

// handleLiquidstakeBalance GET /api/liquidstake/balance?address=
func (s *RPCServer) handleLiquidstakeBalance(w http.ResponseWriter, r *http.Request) {
	if s.node.ls == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "liquidstake_disabled"})
		return
	}
	addr := r.URL.Query().Get("address")
	if addr == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "address_required"})
		return
	}
	acct, redeemable, err := s.node.ls.Balance(addr)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "found": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "found": true,
		"address": acct.Address, "staked": acct.Staked,
		"stTACm": acct.StTACm, "redeemable_tacm": redeemable,
	})
}

// handleLiquidstakeRateHistory GET /api/liquidstake/rate-history?limit=N
// 回後端持久化的匯率歷史（v76：非前端本機，跨用戶共享）。
func (s *RPCServer) handleLiquidstakeRateHistory(w http.ResponseWriter, r *http.Request) {
	ls := s.node.ls
	if ls == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "liquidstake_disabled"})
		return
	}
	limit := 180
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	pts, err := ls.RateHistory(limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "points": pts})
}

// handleLiquidstakeStats GET /api/liquidstake/stats
func (s *RPCServer) handleLiquidstakeStats(w http.ResponseWriter, r *http.Request) {
	if s.node.ls == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "liquidstake_disabled"})
		return
	}
	st := s.node.ls.Stats()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"total_staked": st.TotalStaked, "total_sttacm": st.TotalStTACm,
		"exchange_rate": st.ExchangeRate, "apy": st.APY,
		"reward_reserve": st.RewardReserve, "depositors": st.Depositors,
		"redeemable_tacm": st.RedeemableTACm,
	})
}

// handleLiquidstakeRewards POST /api/liquidstake/rewards {amount}
// 節點委託收益注入（管理/內部）：匯率上升。
func (s *RPCServer) handleLiquidstakeRewards(w http.ResponseWriter, r *http.Request) {
	if s.node.ls == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "liquidstake_disabled"})
		return
	}
	var req struct {
		Amount float64 `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_request"})
		return
	}
	if err := s.node.ls.AddRewards(req.Amount); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rate": s.node.ls.ExchangeRate()})
}

// handleLiquidstakeSetAPY POST /api/liquidstake/apy {apy}
func (s *RPCServer) handleLiquidstakeSetAPY(w http.ResponseWriter, r *http.Request) {
	if s.node.ls == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "liquidstake_disabled"})
		return
	}
	var req struct {
		APY float64 `json:"apy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_request"})
		return
	}
	if err := s.node.ls.SetAPY(req.APY); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "apy": req.APY})
}

var _ = strconv.Itoa // keep import if unused path changes
