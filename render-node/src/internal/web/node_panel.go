package web

// M148：節點架設者面板——架設者操作自己的節點：
//  1. 查看節點狀態與節點獎勵餘額（coinbase:node 9% 累計）
//  2. 設置管理密碼（保護綁定/提領操作）
//  3. 綁定收款地址（tac web3 錢包地址）
//  4. 提領：節點私鑰簽名把節點獎勵轉入綁定錢包（上鏈、可審計）

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/flosch/pongo2/v6"

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

// nodePanelView 回傳給 /api/node/panel 的節點面板數據。
type nodePanelView struct {
	NodeID        string `json:"node_id"`
	NodeAddress   string `json:"node_address"`
	Balance       string `json:"balance"` // 節點地址鏈上餘額（節點獎勵累計）
	PayoutAddress string `json:"payout_address"`
	AdminSet      bool   `json:"admin_set"`
	Height        int64  `json:"height"`
}

// handleNodePanel GET /node-panel — 節點架設者面板頁面。
func (s *Server) handleNodePanel(w http.ResponseWriter, r *http.Request) {
	s.render(w, "node_panel.html", pongo2.Context{})
}

// handleNodePanelAPI GET /api/node/panel — 節點架設者面板狀態。
func (s *Server) handleNodePanelAPI(w http.ResponseWriter, r *http.Request) {
	dataDir := s.ds.NodeDataDir()
	cfg := loadNodeConfig(dataDir)
	v := nodePanelView{
		NodeID:        cfg.NodeID,
		NodeAddress:   s.ds.NodeAddress(),
		Balance:       s.ds.NodeBalance(),
		PayoutAddress: cfg.PayoutAddress,
		AdminSet:      cfg.AdminPassHash != "",
		Height:        s.ds.Status().Height,
	}
	if v.NodeID == "" {
		v.NodeID = "anchor"
	}
	writeJSON(w, v)
}

// handleNodeSetupAPI POST /api/node/setup — 首次設置管理密碼（已設置則拒絕）。
// body: {"admin_pass": "..."}
func (s *Server) handleNodeSetupAPI(w http.ResponseWriter, r *http.Request) {
	dataDir := s.ds.NodeDataDir()
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
		cfg.NodeID = "anchor"
	}
	if err := saveNodeConfig(dataDir, cfg); err != nil {
		writeErrJSON(w, http.StatusInternalServerError, "儲存配置失敗: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": "管理密碼已設置"})
}

// verifyAdminPass 驗證管理密碼（未設置＝拒絕一切寫操作）。
func (s *Server) verifyAdminPass(dataDir, pass string) bool {
	cfg := loadNodeConfig(dataDir)
	if cfg.AdminPassHash == "" {
		return false
	}
	return cfg.AdminPassHash == hashAdminPass(pass)
}

// handleNodeBindAPI POST /api/node/bind — 綁定 tac web3 錢包收款地址。
// body: {"admin_pass": "...", "payout_address": "tx01..."}
func (s *Server) handleNodeBindAPI(w http.ResponseWriter, r *http.Request) {
	dataDir := s.ds.NodeDataDir()
	var req struct {
		AdminPass     string `json:"admin_pass"`
		PayoutAddress string `json:"payout_address"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrJSON(w, http.StatusBadRequest, "請求格式錯誤")
		return
	}
	if !s.verifyAdminPass(dataDir, req.AdminPass) {
		writeErrJSON(w, http.StatusUnauthorized, "管理密碼錯誤")
		return
	}
	addr := strings.TrimSpace(req.PayoutAddress)
	if !crypto.IsValidAddress(addr) {
		writeErrJSON(w, http.StatusBadRequest, "收款地址格式無效（需為 TACm web3 錢包地址）")
		return
	}
	cfg := loadNodeConfig(dataDir)
	cfg.PayoutAddress = addr
	if err := saveNodeConfig(dataDir, cfg); err != nil {
		writeErrJSON(w, http.StatusInternalServerError, "儲存配置失敗: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "payout_address": addr})
}

// handleNodeWithdrawAPI POST /api/node/withdraw — 提領節點獎勵到綁定錢包（節點私鑰簽名上鏈）。
// body: {"admin_pass": "...", "amount": "1.5"}
func (s *Server) handleNodeWithdrawAPI(w http.ResponseWriter, r *http.Request) {
	dataDir := s.ds.NodeDataDir()
	var req struct {
		AdminPass string `json:"admin_pass"`
		Amount    string `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrJSON(w, http.StatusBadRequest, "請求格式錯誤")
		return
	}
	if !s.verifyAdminPass(dataDir, req.AdminPass) {
		writeErrJSON(w, http.StatusUnauthorized, "管理密碼錯誤")
		return
	}
	cfg := loadNodeConfig(dataDir)
	if cfg.PayoutAddress == "" {
		writeErrJSON(w, http.StatusBadRequest, "尚未綁定收款地址，請先綁定 tac web3 錢包地址")
		return
	}
	amt := strings.TrimSpace(req.Amount)
	if amt == "" {
		writeErrJSON(w, http.StatusBadRequest, "請輸入提領金額")
		return
	}
	// 金額必須為正數且不得超過節點餘額。
	amountF, err := strconv.ParseFloat(amt, 64)
	if err != nil || amountF <= 0 {
		writeErrJSON(w, http.StatusBadRequest, "提領金額無效")
		return
	}
	balance, err := strconv.ParseFloat(s.ds.NodeBalance(), 64)
	if err == nil && amountF > balance+1e-9 {
		writeErrJSON(w, http.StatusBadRequest, "提領金額超過節點獎勵餘額")
		return
	}
	// 以節點私鑰簽名發送（SendFromNode：節點地址 → 綁定錢包地址，上鏈可審計）。
	hash, txid, err := s.ds.NodeSendFromNode(cfg.PayoutAddress, amt)
	if err != nil {
		writeErrJSON(w, http.StatusInternalServerError, "提領失敗: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"ok": true, "tx_hash": hash, "txid": txid,
		"message": "提領交易已提交上鏈",
	})
}
