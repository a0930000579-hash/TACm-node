package node

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"tacm/internal/crypto"
	"tacm/internal/vm"
)

// TACGovernor 鏈上治理 RPC（P1-③，DAO）：
//   - POST /tacgov/deploy   —— 節點代簽部署 TACGovernor（部署者＝節點 keypair，slot2）
//   - GET  /tacgov/status   —— 回傳部署狀態、合約地址、下一個 proposal id
//   - POST /tacgov/propose  —— {target, value} 節點代簽 propose（任何帳戶可提案）
//   - POST /tacgov/vote     —— {proposal_id, support} 節點代簽 vote（投票者＝提交節點，
//                              一人一票；support=1 贊成 / 0 反對）
//   - POST /tacgov/execute  —— {proposal_id} 節點代簽 execute（多數決 support>oppose）
//   - GET  /tacgov/query    —— ?proposal_id=N&op=info|support|oppose|executed
//   - POST /tacgov/calldata —— {op, target?, value?, proposal_id?, support?}
//                              回傳 {to, data}，供自備私鑰簽名後 POST /tx/submit 上鏈
//
// 治理規則：任何帳戶可提案；投票為一人一票（每個地址一票，重複投票 revert）；
// support > oppose 且未執行時可由任何人 execute，對目標地址執行 TACm 轉帳。
// 目前真實上鏈投票以提交節點為投票者（節點代簽）；多簽名投票為後續階段。

// govSimCall 對 TACGovernor 合約執行只讀模擬呼叫，回傳原始回傳資料。
func (s *RPCServer) govSimCall(evm, caller0x string, calldata []byte) ([]byte, error) {
	caller := caller0x
	if caller == "" {
		caller = "0x0000000000000000000000000000000000000000"
	}
	r, err := s.node.contracts.SimulateCallGas(evm, caller, calldata, new(big.Int), s.node.contracts.DefaultGas())
	if err != nil {
		return nil, err
	}
	if r.Reverted || !r.OK {
		return nil, fmt.Errorf("tacgov: 呼叫被拒 reverted=%v err=%v", r.Reverted, r.Error)
	}
	return []byte(strings.TrimPrefix(r.ReturnData, "0x")), nil
}

// govContract 回傳已部署的 TACGovernor 地址（0x 格式）。
func (s *RPCServer) govContract() (string, error) {
	evm := s.node.tacgovAddr
	if evm == "" {
		return "", fmt.Errorf("tacgov: 治理合約尚未部署，請先 POST /tacgov/deploy")
	}
	return evm, nil
}

// govSubmitTx 共用：節點代簽一筆對 TACGovernor 的合約呼叫交易。
func (s *RPCServer) govSubmitTx(w http.ResponseWriter, evm string, calldata []byte) bool {
	kp := s.node.keypair
	if kp == nil {
		writeErr(w, http.StatusServiceUnavailable, "node key unavailable")
		return false
	}
	from, err := kp.Address()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return false
	}
	nonce := s.node.db.GetNonce(from) + s.node.pendingTxCount(from)
	to, err := evmToTx0(evm)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return false
	}
	tx := txBodyForCall(from, to, nonce, calldata, kp)
	sig, err := crypto.SignTransaction(tx, kp.PrivateKey())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return false
	}
	tx["signature"] = sig
	txHash, err := s.node.SubmitTransaction(tx)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return false
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "tx_hash": txHash, "status": "mempool",
		"contract_address": to,
	})
	return true
}

// handleTACGovDeploy POST /tacgov/deploy
func (s *RPCServer) handleTACGovDeploy(w http.ResponseWriter, r *http.Request) {
	kp := s.node.keypair
	if kp == nil {
		writeErr(w, http.StatusServiceUnavailable, "node key unavailable")
		return
	}
	from, err := kp.Address()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	nonce := s.node.db.GetNonce(from) + s.node.pendingTxCount(from)
	evmFrom, err := tx0To0x(from)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	addr0x := vm.CreateAddress(evmFrom, uint64(nonce))
	to, err := evmToTx0(addr0x)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	init := vm.TACGovernorInit()
	memo := vmDeployPrefix + hex.EncodeToString(init)
	tx := map[string]any{
		"from": from, "to": to,
		"amount": "0", "fee": "0",
		"nonce": nonce, "ts": time.Now().Unix(),
		"pubkey": hex.EncodeToString(kp.PublicKeyCompressed()),
		"memo":   memo,
	}
	sig, err := crypto.SignTransaction(tx, kp.PrivateKey())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	tx["signature"] = sig
	txHash, err := s.node.SubmitTransaction(tx)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.node.tacgovAddr = addr0x
	s.node.govProposalCount = 0
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "tx_hash": txHash, "status": "mempool",
		"contract_address": to, "evm_address": addr0x, "creator": from,
		"name": "TAC Governance", "standard": "DAO propose/vote/execute",
	})
}

// handleTACGovStatus GET /tacgov/status
func (s *RPCServer) handleTACGovStatus(w http.ResponseWriter, r *http.Request) {
	evm := s.node.tacgovAddr
	out := map[string]any{
		"deployed": evm != "",
		"ok":       true,
		"name":     "TAC Governance",
		"standard": "DAO propose/vote/execute",
	}
	if evm != "" {
		out["contract_address"] = evm
		out["next_proposal_id"] = s.node.govProposalCount + 1
	}
	writeJSON(w, http.StatusOK, out)
}

// handleTACGovPropose POST /tacgov/propose {target, value}
func (s *RPCServer) handleTACGovPropose(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target string `json:"target"`
		Value  string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "tacgov: 無效 JSON 請求體")
		return
	}
	evm, err := s.govContract()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	tw, err := vm.TACGovTargetWord(req.Target)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	val, err := parseBigWord(req.Value)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	calldata := vm.TACGovProposeCalldata(tw, val)
	if !s.govSubmitTx(w, evm, calldata) {
		return
	}
	// 記憶體記錄下一個 pid（入塊後以鏈上為準）
	s.node.govProposalCount++
}

// handleTACGovVote POST /tacgov/vote {proposal_id, support}
func (s *RPCServer) handleTACGovVote(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProposalID int64  `json:"proposal_id"`
		Support    *int64 `json:"support"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "tacgov: 無效 JSON 請求體")
		return
	}
	if req.ProposalID < 1 || req.Support == nil || (*req.Support != 0 && *req.Support != 1) {
		writeErr(w, http.StatusBadRequest, "tacgov: proposal_id 需 ≥1，support 需為 0 或 1")
		return
	}
	evm, err := s.govContract()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	calldata := vm.TACGovVoteCalldata(big.NewInt(req.ProposalID), big.NewInt(*req.Support))
	if !s.govSubmitTx(w, evm, calldata) {
		return
	}
}

// handleTACGovExecute POST /tacgov/execute {proposal_id}
func (s *RPCServer) handleTACGovExecute(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProposalID int64 `json:"proposal_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "tacgov: 無效 JSON 請求體")
		return
	}
	if req.ProposalID < 1 {
		writeErr(w, http.StatusBadRequest, "tacgov: proposal_id 需 ≥1")
		return
	}
	evm, err := s.govContract()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	calldata := vm.TACGovExecuteCalldata(big.NewInt(req.ProposalID))
	if !s.govSubmitTx(w, evm, calldata) {
		return
	}
}

// handleTACGovQuery GET /tacgov/query?proposal_id=N&op=info|support|oppose|executed|target|value|creator
func (s *RPCServer) handleTACGovQuery(w http.ResponseWriter, r *http.Request) {
	evm, err := s.govContract()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	pidStr := r.URL.Query().Get("proposal_id")
	pid, err := parseBigWord(pidStr)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "tacgov: proposal_id 無效")
		return
	}
	if pid.Sign() < 1 {
		writeErr(w, http.StatusBadRequest, "tacgov: proposal_id 需 ≥1")
		return
	}
	op := r.URL.Query().Get("op")
	if op == "" {
		op = "info"
	}
	raw, err := s.govSimCall(evm, "", vm.TACGovInfoCalldata(pid))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rd, herr := hex.DecodeString(strings.TrimPrefix(string(raw), "0x"))
	if herr != nil || len(rd) < 192 {
		writeErr(w, http.StatusInternalServerError, "tacgov: 回傳資料無效")
		return
	}
	creator := "0x" + hex.EncodeToString(rd[12:32])
	target := "0x" + hex.EncodeToString(rd[140:160])
	support := new(big.Int).SetBytes(rd[32:64]).Int64()
	oppose := new(big.Int).SetBytes(rd[64:96]).Int64()
	executed := new(big.Int).SetBytes(rd[96:128]).Int64()
	value := new(big.Int).SetBytes(rd[160:192]).Int64()
	all := map[string]any{
		"proposal_id":  pid.Int64(),
		"creator":      creator,
		"support":      support,
		"oppose":       oppose,
		"executed":     executed,
		"target":       target,
		"value":        value,
		"status":       "executed",
		"passed":       support > oppose,
	}
	if executed == 0 {
		all["status"] = "pending"
	}
	switch op {
	case "support":
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "support": support})
	case "oppose":
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "oppose": oppose})
	case "executed":
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "executed": executed})
	case "target":
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "target": target, "value": value})
	case "creator":
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "creator": creator})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "proposal": all})
	}
}

// handleTACGovCalldata POST /tacgov/calldata
// {op:"propose", target, value} | {op:"vote", proposal_id, support} |
// {op:"execute", proposal_id} | {op:"info", proposal_id}
func (s *RPCServer) handleTACGovCalldata(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Op         string `json:"op"`
		Target     string `json:"target"`
		Value      string `json:"value"`
		ProposalID int64  `json:"proposal_id"`
		Support    *int64 `json:"support"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "tacgov: 無效 JSON 請求體")
		return
	}
	evm, err := s.govContract()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	to, err := evmToTx0(evm)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var data []byte
	switch req.Op {
	case "propose":
		tw, e := vm.TACGovTargetWord(req.Target)
		if e != nil {
			writeErr(w, http.StatusBadRequest, e.Error())
			return
		}
		val, e := parseBigWord(req.Value)
		if e != nil {
			writeErr(w, http.StatusBadRequest, e.Error())
			return
		}
		data = vm.TACGovProposeCalldata(tw, val)
	case "vote":
		if req.ProposalID < 1 || req.Support == nil || (*req.Support != 0 && *req.Support != 1) {
			writeErr(w, http.StatusBadRequest, "tacgov: proposal_id 需 ≥1，support 需為 0 或 1")
			return
		}
		data = vm.TACGovVoteCalldata(big.NewInt(req.ProposalID), big.NewInt(*req.Support))
	case "execute":
		if req.ProposalID < 1 {
			writeErr(w, http.StatusBadRequest, "tacgov: proposal_id 需 ≥1")
			return
		}
		data = vm.TACGovExecuteCalldata(big.NewInt(req.ProposalID))
	case "info":
		if req.ProposalID < 1 {
			writeErr(w, http.StatusBadRequest, "tacgov: proposal_id 需 ≥1")
			return
		}
		data = vm.TACGovInfoCalldata(big.NewInt(req.ProposalID))
	default:
		writeErr(w, http.StatusBadRequest, "tacgov: op 需為 propose|vote|execute|info")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "to": to, "evm_address": evm,
		"data": "0x" + hex.EncodeToString(data),
	})
}
