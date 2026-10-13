package node

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// governanceProposeReq POST /api/governance/propose {title,desc,ptype,param_key,param_value,address}
type governanceProposeReq struct {
	Title      string `json:"title"`
	Desc       string `json:"desc"`
	PType      string `json:"ptype"`
	ParamKey   string `json:"param_key"`
	ParamValue string `json:"param_value"`
	Address    string `json:"address"`
}

// governanceVoteReq POST /api/governance/vote {proposal_id,choice,address}
type governanceVoteReq struct {
	ProposalID int64  `json:"proposal_id"`
	Choice     string `json:"choice"`
	Address    string `json:"address"`
}

// handleGovernanceProposals GET /api/governance/proposals
// 回傳全部提案（新→舊）與可治理參數。
func (s *RPCServer) handleGovernanceProposals(w http.ResponseWriter, r *http.Request) {
	gov := s.node.Governance()
	if gov == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "governance_disabled"})
		return
	}
	list, err := gov.List()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "proposals": list, "params": gov.Params()})
}

// handleGovernanceParams GET /api/governance/params
// 回傳可治理參數現值。
func (s *RPCServer) handleGovernanceParams(w http.ResponseWriter, r *http.Request) {
	gov := s.node.Governance()
	if gov == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "governance_disabled"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "params": gov.Params()})
}

// handleGovernancePropose POST /api/governance/propose
// 發起提案：校驗地址與參數後寫入治理庫（投票窗口=20 區塊）。
func (s *RPCServer) handleGovernancePropose(w http.ResponseWriter, r *http.Request) {
	gov := s.node.Governance()
	if gov == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "governance_disabled"})
		return
	}
	var req governanceProposeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_json"})
		return
	}
	req.Address = strings.TrimSpace(req.Address)
	if !validTACAddress(req.Address) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_address"})
		return
	}
	tip := s.node.DB().GetTipHeight()
	id, err := gov.Propose(req.Title, req.Desc, req.PType, req.ParamKey, req.ParamValue, req.Address, tip, "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "proposal_id": id})
}

// handleGovernanceVote POST /api/governance/vote
// 投票：服務端計算投票權重（1 基本票 + 持幣加成，上限 10），同人同案僅一次。
func (s *RPCServer) handleGovernanceVote(w http.ResponseWriter, r *http.Request) {
	gov := s.node.Governance()
	if gov == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "governance_disabled"})
		return
	}
	var req governanceVoteReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_json"})
		return
	}
	req.Address = strings.TrimSpace(req.Address)
	if !validTACAddress(req.Address) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_address"})
		return
	}
	if req.ProposalID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_proposal_id"})
		return
	}
	// 投票權重：1 基本票 + 持幣加成（TACm 餘額每 1,000,000 加 1，上限 9）。
	power := 1.0
	if bal, err := strconv.ParseFloat(s.node.DB().GetBalance(req.Address), 64); err == nil && bal > 0 {
		bonus := bal / 1000000
		if bonus > 9 {
			bonus = 9
		}
		power += bonus
	}
	tip := s.node.DB().GetTipHeight()
	if err := gov.Vote(req.ProposalID, req.Address, req.Choice, power, tip, ""); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "proposal_id": req.ProposalID, "power": power})
}

// validTACAddress 輕量地址格式檢查（tx0 開頭或獎勵池）。
func validTACAddress(addr string) bool {
	if len(addr) < 10 || len(addr) > 64 {
		return false
	}
	return strings.HasPrefix(addr, "tx0") || strings.HasPrefix(addr, "reward_pool")
}
