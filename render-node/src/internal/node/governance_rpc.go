package node

import (
	"encoding/json"
	"math/big"
	"net/http"
	"strconv"
	"strings"

	"tacm/internal/wallet"
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

// walletBalanceOf 回傳地址錢包服務 TACm 餘額（raw wei → TACM float，M151 投票權重）。
// 權威帳本為 wallet store（錢包頁/交易所/挖礦收益均寫入此層），非 VM accounts 表。
func walletBalanceOf(ws *wallet.Service, addr string) float64 {
	if ws == nil {
		return 0
	}
	acc, err := ws.Balance(addr)
	if err != nil || acc == nil {
		return 0
	}
	f, _ := new(big.Float).SetInt(acc.TACmBalance).Float64()
	return f / 1e18
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

// handleGovernanceVotes GET /api/governance/votes?proposal_id=N
// 回傳指定提案的投票明細（投票人/權重/選擇/區塊）。
func (s *RPCServer) handleGovernanceVotes(w http.ResponseWriter, r *http.Request) {
	gov := s.node.Governance()
	if gov == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "governance_disabled"})
		return
	}
	pid, err := strconv.ParseInt(r.URL.Query().Get("proposal_id"), 10, 64)
	if err != nil || pid <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid proposal_id"})
		return
	}
	list, err := gov.Votes(pid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "proposal_id": pid, "votes": list})
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
	power := walletBalanceOf(s.node.walletSvc, req.Address)
	id, err := gov.Propose(req.Title, req.Desc, req.PType, req.ParamKey, req.ParamValue, req.Address, power, tip, "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "proposal_id": id, "power": power})
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
	// 投票權重（M151）：鏈上 TACm 餘額直接加權（1 TACM = 1 票，與主流 DAO 一致）。
	// v129 委託投票：已委託者無權自投（權力歸受託人）；受託人權力＝自己餘額＋全部委託者餘額。
	if de := gov.DelegateeOf(req.Address); de != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "already_delegated_vote_via_delegatee"})
		return
	}
	power := walletBalanceOf(s.node.walletSvc, req.Address)
	for _, delegator := range gov.DelegatorsOf(req.Address) {
		power += walletBalanceOf(s.node.walletSvc, delegator)
	}
	if power <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "zero_balance_no_voting_power"})
		return
	}
	tip := s.node.DB().GetTipHeight()
	if err := gov.Vote(req.ProposalID, req.Address, req.Choice, power, tip, ""); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "proposal_id": req.ProposalID, "power": power})
}

// handleGovernanceConfig GET /api/governance/config
// 回傳治理參數（M151：投票窗口/通過門檻/法定人數/時間鎖/提案門檻）。
func (s *RPCServer) handleGovernanceConfig(w http.ResponseWriter, r *http.Request) {
	gov := s.node.Governance()
	if gov == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "governance_disabled"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "config": gov.Config()})
}

// validTACAddress 輕量地址格式檢查（tx0 開頭或獎勵池）。
func validTACAddress(addr string) bool {
	if len(addr) < 10 || len(addr) > 64 {
		return false
	}
	return strings.HasPrefix(addr, "tx0") || strings.HasPrefix(addr, "reward_pool")
}

// governanceDelegateReq POST /api/governance/delegate {address, delegatee}
type governanceDelegateReq struct {
	Address   string `json:"address"`
	Delegatee string `json:"delegatee"`
}

// handleGovernanceDelegate POST /api/governance/delegate
// 委託投票（v129）：address 把自己的投票權委託給 delegatee（覆寫式，與主流 DAO 一致）。
func (s *RPCServer) handleGovernanceDelegate(w http.ResponseWriter, r *http.Request) {
	gov := s.node.Governance()
	if gov == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "governance_disabled"})
		return
	}
	var req governanceDelegateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_json"})
		return
	}
	req.Address = strings.TrimSpace(req.Address)
	req.Delegatee = strings.TrimSpace(req.Delegatee)
	if !validTACAddress(req.Address) || !validTACAddress(req.Delegatee) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_address"})
		return
	}
	tip := s.node.DB().GetTipHeight()
	if err := gov.Delegate(req.Address, req.Delegatee, tip, ""); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "delegator": req.Address, "delegatee": req.Delegatee})
}

// governanceUndelegateReq POST /api/governance/undelegate {address}
type governanceUndelegateReq struct {
	Address string `json:"address"`
}

// handleGovernanceUndelegate POST /api/governance/undelegate
// 解除委託（v129）：address 收回自己的投票權。
func (s *RPCServer) handleGovernanceUndelegate(w http.ResponseWriter, r *http.Request) {
	gov := s.node.Governance()
	if gov == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "governance_disabled"})
		return
	}
	var req governanceUndelegateReq
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
	if err := gov.Undelegate(req.Address, tip, ""); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "delegator": req.Address})
}

// handleGovernanceDelegations GET /api/governance/delegations
// 回傳全部委託紀錄（v129）。
func (s *RPCServer) handleGovernanceDelegations(w http.ResponseWriter, r *http.Request) {
	gov := s.node.Governance()
	if gov == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "governance_disabled"})
		return
	}
	list, err := gov.Delegations()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "delegations": list})
}
