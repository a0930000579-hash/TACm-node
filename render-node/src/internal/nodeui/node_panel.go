package nodeui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"tacm/internal/crypto"
)

// NodeConfig 節點面板配置，存 data-dir/node_config.json（節點本地、架設者專用）。
type NodeConfig struct {
	NodeID        string `json:"node_id"`
	PayoutAddress string `json:"payout_address"`  // 綁定的 tac web3 錢包收款地址
	AdminPassHash string `json:"admin_pass_hash"` // 管理密碼 SHA-256 hex（首次設置後保護寫操作）
}

func nodeConfigPath(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "node_config.json")
}

func loadNodeConfig(dataDir string) *NodeConfig {
	p := nodeConfigPath(dataDir)
	if p == "" {
		return &NodeConfig{}
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return &NodeConfig{}
	}
	var c NodeConfig
	if json.Unmarshal(b, &c) != nil {
		return &NodeConfig{}
	}
	return &c
}

func saveNodeConfig(dataDir string, c *NodeConfig) error {
	p := nodeConfigPath(dataDir)
	if p == "" {
		return os.ErrInvalid
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

func hashAdminPass(pass string) string {
	h := sha256.Sum256([]byte(pass))
	return hex.EncodeToString(h[:])
}

// handleNodePanel GET /node-panel — 節點架設者面板頁面（純節點，無 DApp）。
func (s *Server) handleNodePanel(w http.ResponseWriter, r *http.Request) {
	raw, err := assets.ReadFile("templates/node_panel.html")
	if err != nil {
		http.Error(w, "panel template missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(raw)
}

// handleNodePanelAPI GET /api/node/panel — 面板狀態。
func (s *Server) handleNodePanelAPI(w http.ResponseWriter, r *http.Request) {
	dataDir := s.n.DataDir()
	cfg := loadNodeConfig(dataDir)
	addr := s.n.Address()
	v := map[string]any{
		"node_id":        cfg.NodeID,
		"node_address":   addr,
		"balance":        s.n.DB().GetBalance(addr),
		"payout_address": cfg.PayoutAddress,
		"admin_set":      cfg.AdminPassHash != "",
		"height":         s.n.DB().GetTipHeight(),
	}
	if v["node_id"] == "" {
		v["node_id"] = s.nodeID
	}
	writeJSON(w, v)
}

// handleNodeSetupAPI POST /api/node/setup — 首次設置管理密碼。
func (s *Server) handleNodeSetupAPI(w http.ResponseWriter, r *http.Request) {
	dataDir := s.n.DataDir()
	cfg := loadNodeConfig(dataDir)
	if cfg.AdminPassHash != "" {
		writeErrJSON(w, http.StatusBadRequest, "管理密碼已設置，不可重複設置")
		return
	}
	var req struct {
		AdminPass string `json:"admin_pass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrJSON(w, http.StatusBadRequest, "請求格式錯誤")
		return
	}
	pass := strings.TrimSpace(req.AdminPass)
	if len(pass) < 6 {
		writeErrJSON(w, http.StatusBadRequest, "管理密碼至少 6 個字元")
		return
	}
	cfg.AdminPassHash = hashAdminPass(pass)
	if cfg.NodeID == "" {
		cfg.NodeID = s.nodeID
	}
	if err := saveNodeConfig(dataDir, cfg); err != nil {
		writeErrJSON(w, http.StatusInternalServerError, "儲存配置失敗: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": "管理密碼已設置"})
}

// verifyAdminPass 驗證管理密碼（未設置＝拒絕一切寫操作）。
func (s *Server) verifyAdminPass(pass string) bool {
	cfg := loadNodeConfig(s.n.DataDir())
	if cfg.AdminPassHash == "" {
		return false
	}
	return cfg.AdminPassHash == hashAdminPass(pass)
}

// handleNodeBindAPI POST /api/node/bind — 綁定收款地址。
func (s *Server) handleNodeBindAPI(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AdminPass     string `json:"admin_pass"`
		PayoutAddress string `json:"payout_address"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrJSON(w, http.StatusBadRequest, "請求格式錯誤")
		return
	}
	if !s.verifyAdminPass(req.AdminPass) {
		writeErrJSON(w, http.StatusUnauthorized, "管理密碼錯誤")
		return
	}
	addr := strings.TrimSpace(req.PayoutAddress)
	if !crypto.IsValidAddress(addr) {
		writeErrJSON(w, http.StatusBadRequest, "收款地址格式無效（需為 TACm web3 錢包地址）")
		return
	}
	cfg := loadNodeConfig(s.n.DataDir())
	cfg.PayoutAddress = addr
	if err := saveNodeConfig(s.n.DataDir(), cfg); err != nil {
		writeErrJSON(w, http.StatusInternalServerError, "儲存配置失敗: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "payout_address": addr})
}

// handleNodeWithdrawAPI POST /api/node/withdraw — 提領節點獎勵到綁定錢包（節點私鑰簽名上鏈）。
func (s *Server) handleNodeWithdrawAPI(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AdminPass string `json:"admin_pass"`
		Amount    string `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrJSON(w, http.StatusBadRequest, "請求格式錯誤")
		return
	}
	if !s.verifyAdminPass(req.AdminPass) {
		writeErrJSON(w, http.StatusUnauthorized, "管理密碼錯誤")
		return
	}
	cfg := loadNodeConfig(s.n.DataDir())
	if cfg.PayoutAddress == "" {
		writeErrJSON(w, http.StatusBadRequest, "尚未綁定收款地址，請先綁定 tac web3 錢包地址")
		return
	}
	amt := strings.TrimSpace(req.Amount)
	if amt == "" {
		writeErrJSON(w, http.StatusBadRequest, "請輸入提領金額")
		return
	}
	amountF, err := strconv.ParseFloat(amt, 64)
	if err != nil || amountF <= 0 {
		writeErrJSON(w, http.StatusBadRequest, "提領金額無效")
		return
	}
	addr := s.n.Address()
	balance, err := strconv.ParseFloat(s.n.DB().GetBalance(addr), 64)
	if err == nil && amountF > balance+1e-9 {
		writeErrJSON(w, http.StatusBadRequest, "提領金額超過節點獎勵餘額")
		return
	}
	// 節點私鑰簽名發送（節點地址 → 綁定錢包地址，上鏈可審計）。
	hash, txid, err := s.n.SendFromNode(cfg.PayoutAddress, amt)
	if err != nil {
		writeErrJSON(w, http.StatusInternalServerError, "提領失敗: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"ok": true, "tx_hash": hash, "txid": txid,
		"message": "提領交易已提交上鏈",
	})
}

func writeErrJSON(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": msg})
}
