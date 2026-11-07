package node

// ens_rpc.go — ENS 鏈上域名 RPC（對標 ENS/Ethereum Name Service）：
//   POST /api/ens/register {name, address, target?, years} — 註冊/續期 .tac 名稱（年費 TACm 進獎勵池）
//   GET  /api/ens/resolve?name=xxx        — 名稱→目標地址
//   GET  /api/ens/reverse?address=        — 地址→名稱
//   GET  /api/ens/my?address=             — 地址名下域名清單
// 名稱規範：[a-z0-9-] ≤63 字元；namehash 依 ENS 標準（keccak 層疊，尾綴 .tac）。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"tacm/internal/ens"
	"tacm/internal/wallet"
)

// ensFeePerYearTACM ENS 年費（TACm／年），費用進獎勵池（reward_pool）。
const ensFeePerYearTACM = 1.0

// handleENSRegister POST /api/ens/register
func (s *RPCServer) handleENSRegister(w http.ResponseWriter, r *http.Request) {
	if s.node.ensSvc == nil || s.node.walletSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "ENS 未啟用")
		return
	}
	var req struct {
		Name    string `json:"name"`
		Address string `json:"address"`
		Target  string `json:"target"`
		Years   int64  `json:"years"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	req.Name = strings.TrimSpace(strings.ToLower(req.Name))
	req.Address = strings.TrimSpace(req.Address)
	req.Target = strings.TrimSpace(req.Target)
	if req.Years < 1 {
		req.Years = 1
	}
	if req.Years > 100 {
		req.Years = 100
	}
	if !ens.ValidName(req.Name) {
		writeErr(w, http.StatusBadRequest, "名稱不合法（僅 [a-z0-9-]，≤63 字元）")
		return
	}
	if req.Address == "" {
		writeErr(w, http.StatusBadRequest, "缺少註冊地址")
		return
	}
	if req.Target != "" {
		if _, err := s.node.normalizeEthAddr(req.Target); err != nil {
			writeErr(w, http.StatusBadRequest, "target 地址無效")
			return
		}
	}

	// 年費：years × ensFeePerYearTACM TACm，從註冊者轉入獎勵池。
	fee := float64(req.Years) * ensFeePerYearTACM
	if _, err := s.node.walletSvc.Transfer(req.Address, "reward_pool", wallet.AssetTACm,
		amountOf(fee, "TACm"), "ens:register:"+req.Name); err != nil {
		writeErr(w, http.StatusBadRequest, "年費支付失敗（需 "+strconv.FormatFloat(fee, 'f', 2, 64)+" TACm）："+err.Error())
		return
	}

	rec, err := s.node.ensSvc.Register(req.Name, req.Address, req.Target, req.Years)
	if err != nil {
		// 記錄失敗：退回年費。
		_ = s.node.walletSvc.InternalTransfer("reward_pool", req.Address, wallet.AssetTACm,
			amountOf(fee, "TACm"), "ens:refund:"+req.Name)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "name": rec.Name + ".tac", "node": rec.Node,
		"owner": rec.Owner, "target": rec.Target,
		"expires_at": rec.ExpiresAt, "years": req.Years, "fee": fee,
		"message": "註冊成功",
	})
}

// handleENSResolve GET /api/ens/resolve?name=xxx
func (s *RPCServer) handleENSResolve(w http.ResponseWriter, r *http.Request) {
	if s.node.ensSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "ENS 未啟用")
		return
	}
	name := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("name")))
	name = strings.TrimSuffix(name, ".tac")
	rec, err := s.node.ensSvc.Resolve(name)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	addr := rec.Target
	if addr == "" {
		addr = rec.Owner
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "name": name + ".tac", "owner": rec.Owner, "address": addr,
		"expires_at": rec.ExpiresAt,
	})
}

// handleENSReverse GET /api/ens/reverse?address=xxx
func (s *RPCServer) handleENSReverse(w http.ResponseWriter, r *http.Request) {
	if s.node.ensSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "ENS 未啟用")
		return
	}
	addr := strings.TrimSpace(r.URL.Query().Get("address"))
	name, err := s.node.ensSvc.Reverse(addr)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": "", "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": name + ".tac", "address": addr})
}

// handleENSMy GET /api/ens/my?address=xxx
func (s *RPCServer) handleENSMy(w http.ResponseWriter, r *http.Request) {
	if s.node.ensSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "ENS 未啟用")
		return
	}
	addr := strings.TrimSpace(r.URL.Query().Get("address"))
	names, err := s.node.ensSvc.My(addr)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "names": names})
}

var _ = fmt.Sprintf // 保留（後續擴展）
