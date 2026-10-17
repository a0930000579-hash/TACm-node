package node

// reward_withdraw_rpc.go — M77：節點獎勵提取（架設者領取 coinbase:node 節點獎勵）。
//
// 節點獎勵（M70：每塊 coinbase 9% 歸出塊節點，memo=coinbase:node）自動累計在
// 節點金鑰地址。本端點以節點金鑰代簽一筆轉帳，把節點地址餘額轉到架設者指定地址。
// 安全邊界：節點金鑰是伺服器上的資產，僅允許本機（loopback）呼叫——外部網路
// 不能動走節點餘額。

import (
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"time"

	"tacm/internal/crypto"
)

// handleNodeRewardWithdraw POST /api/node/reward-withdraw {"to":"tx0...","amount":"5"}
// 回傳：{ok, tx_hash, from, to, amount}；非本機 403、參數錯誤 400、餘額不足 submit 錯誤。
func (s *RPCServer) handleNodeRewardWithdraw(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if host != "127.0.0.1" && host != "::1" && host != "localhost" {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"ok": false, "error": "node reward withdraw is local-only: run on the node host",
		})
		return
	}
	var req struct {
		To     string `json:"to"`
		Amount string `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_json"})
		return
	}
	if !crypto.IsValidAddress(req.To) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid to address"})
		return
	}
	amt, err := strconv.ParseFloat(req.Amount, 64)
	if err != nil || amt <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid amount"})
		return
	}
	from, err := s.node.keypair.Address()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "node key unavailable"})
		return
	}
	nonce := s.node.db.GetNonce(from) + s.node.pendingTxCount(from)
	tx := map[string]any{
		"from": from, "to": req.To,
		"amount": req.Amount, "fee": "0",
		"nonce": nonce, "ts": time.Now().Unix(),
		"pubkey": hex.EncodeToString(s.node.keypair.PublicKeyCompressed()),
	}
	sig, err := crypto.SignTransaction(tx, s.node.keypair.PrivateKey())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "sign failed: " + err.Error()})
		return
	}
	tx["signature"] = sig
	hash, err := s.node.SubmitTransaction(tx)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "submit failed: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "tx_hash": hash, "from": from, "to": req.To, "amount": req.Amount,
	})
}
