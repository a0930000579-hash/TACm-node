package node

import (
	"encoding/json"
	"net/http"
	"strconv"

	"tacm/internal/aa"
)

// ---------- M154 帳戶抽象（EIP-4337）RPC ----------

// handleAASubmit POST /aa/submit — 提交 UserOperation（驗證授權後由 Bundler 代執行）。
func (s *RPCServer) handleAASubmit(w http.ResponseWriter, r *http.Request) {
	if s.node.aaSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "aa not initialized")
		return
	}
	var op aa.UserOperation
	if err := json.NewDecoder(r.Body).Decode(&op); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json: "+err.Error())
		return
	}
	// 1) 離線 EntryPoint 驗證：sender 合法、pubkey 匹配、callData hex、ECDSA 簽名。
	if err := s.node.aaSvc.Validate(&op); err != nil {
		s.node.aaSvc.Record(&op, "", "", err.Error())
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// 2) Bundler 代執行：以節點金鑰提交一筆合約呼叫（memo=vm:call:<calldata>）。
	txHash, err := s.node.submitSignedContractCallAs(s.node.keypair, op.Target, op.CallData, op.CallGasLimit)
	if err != nil {
		s.node.aaSvc.Record(&op, "", "", err.Error())
		writeErr(w, http.StatusBadRequest, "bundler_exec: "+err.Error())
		return
	}
	// 3) 估算費用（展示用；v1 由 Bundler 代付 gas，paymaster 機制為 v2 路線）。
	gas, fee, _ := s.node.aaSvc.Estimate(&op)
	s.node.aaSvc.Record(&op, txHash, trimF(fee), "")
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "tx_hash": txHash, "sender": op.Sender,
		"bundler": s.node.aaSvc.Bundler(), "gas": gas, "gas_fee": trimF(fee),
	})
}

// handleAAEstimate POST /aa/estimate — 估算 UserOperation 的 gas 與費用。
func (s *RPCServer) handleAAEstimate(w http.ResponseWriter, r *http.Request) {
	if s.node.aaSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "aa not initialized")
		return
	}
	var op aa.UserOperation
	if err := json.NewDecoder(r.Body).Decode(&op); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json: "+err.Error())
		return
	}
	gas, fee, err := s.node.aaSvc.Estimate(&op)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "gas": gas, "gas_fee": trimF(fee)})
}

// handleAAStatus GET /aa/status?sender=xxx — 查詢 sender 的 AA 操作紀錄。
func (s *RPCServer) handleAAStatus(w http.ResponseWriter, r *http.Request) {
	if s.node.aaSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "aa not initialized")
		return
	}
	sender := r.URL.Query().Get("sender")
	if sender == "" {
		writeErr(w, http.StatusBadRequest, "sender required")
		return
	}
	ops := s.node.aaSvc.Operations(sender)
	if ops == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "operations": []any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "operations": ops})
}

// trimF 格式化浮點費用字串（去除尾零）。
func trimF(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
