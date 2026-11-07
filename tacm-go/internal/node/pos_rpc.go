package node

import (
	"encoding/json"
	"net/http"

	"tacm/internal/consensus/pos"
)

// handlePosValidators M87：動態驗證人集查詢。
// 回傳：PoS 層活躍驗證人（含權重 power、jailed 狀態、質押）+ 當前 BFT 共識集。
func (s *RPCServer) handlePosValidators(w http.ResponseWriter, r *http.Request) {
	if s.node.posStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": false, "validators": []any{}, "consensus": s.node.ValidatorSet().List(),
		})
		return
	}
	vals, err := s.node.posStore.GetActiveValidatorsForConsensus(pos.DefaultConfig().ValidatorCount)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	type row struct {
		Address  string  `json:"address"`
		Name     string  `json:"name"`
		Stake    float64 `json:"stake"`
		Power    int     `json:"power"`
		Status   string  `json:"status"`
		Uptime   float64 `json:"uptime"`
		Proposed int64   `json:"blocks_proposed"`
		Missed   int64   `json:"blocks_missed"`
	}
	out := make([]row, 0, len(vals))
	for i := range vals {
		v := &vals[i]
		p := int(v.TotalStake / powerPerStake)
		if p < 1 {
			p = 1
		}
		out = append(out, row{
			Address: v.Address, Name: v.Name, Stake: v.TotalStake, Power: p,
			Status: v.Status, Uptime: v.Uptime, Proposed: v.BlocksProposed, Missed: v.BlocksMissed,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": true, "validators": out, "consensus": s.node.ValidatorSet().List(),
	})
}

// handlePosRegister M87：註冊成為驗證人（需提供公鑰 hex 供 BFT 校驗）。
func (s *RPCServer) handlePosRegister(w http.ResponseWriter, r *http.Request) {
	if s.node.posStore == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "PoS 層未啟用"})
		return
	}
	var req struct {
		UID        int64   `json:"uid"`
		Address    string  `json:"address"`
		Name       string  `json:"name"`
		Commission float64 `json:"commission"`
		Pubkey     string  `json:"pubkey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON 格式錯誤"})
		return
	}
	if req.UID <= 0 || req.Address == "" || req.Pubkey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "uid/address/pubkey 必填"})
		return
	}
	v, err := s.node.posStore.RegisterValidator(req.UID, req.Address, req.Name, req.Commission, req.Pubkey)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	// 質押後立即重建驗證人集（若已有質押）。
	_ = s.node.RebuildValidatorSetFromPOS()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "validator": v})
}

// handlePosStake M87：驗證人質押（質押即參與共識權重）。
func (s *RPCServer) handlePosStake(w http.ResponseWriter, r *http.Request) {
	if s.node.posStore == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "PoS 層未啟用"})
		return
	}
	var req struct {
		UID         int64   `json:"uid"`
		Address     string  `json:"address"`
		Amount      float64 `json:"amount"`
		ValidatorID *int64  `json:"validator_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON 格式錯誤"})
		return
	}
	if req.UID <= 0 || req.Address == "" || req.Amount <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "uid/address/amount 必填"})
		return
	}
	st, err := s.node.posStore.Stake(req.UID, req.Address, req.Amount, req.ValidatorID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	_ = s.node.RebuildValidatorSetFromPOS()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "stake": st})
}

// handlePosSlashEvidence M87：雙簽罰沒證據列表。
func (s *RPCServer) handlePosSlashEvidence(w http.ResponseWriter, r *http.Request) {
	if s.node.posStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "events": []any{}})
		return
	}
	evs, err := s.node.posStore.GetSlashEvents(50)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "events": evs})
}
