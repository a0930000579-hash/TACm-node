package web

// M148：節點架設者面板——架設者操作自己的節點：
//  1. 查看節點狀態與節點獎勵餘額（coinbase:node 45% 累計）
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
	// 節點獎勵驗證器（M149o）：近 200 塊 coinbase:node 入帳與心跳狀態。
	RewardCount     int64  `json:"reward_count"`
	RewardTotal     string `json:"reward_total"`
	LastRewardBlock int64  `json:"last_reward_block"`
	HeartbeatCount  int64  `json:"heartbeat_count"`
	LastHeartbeat   int64  `json:"last_heartbeat"`
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
	rs := s.ds.NodeRewardStats()
	v.RewardCount = rs.RewardCount
	v.RewardTotal = rs.RewardTotal
	v.LastRewardBlock = rs.LastRewardBlock
	v.HeartbeatCount = rs.HeartbeatCount
	v.LastHeartbeat = rs.LastHeartbeat
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

// handleNodeBootstrapAPI M149m-2：新節點領取引導獎勵（核發 1,000 並自動質押）。
func (s *Server) handleNodeBootstrapAPI(w http.ResponseWriter, r *http.Request) {
	nd, ok := s.ds.(*nodeDS)
	if !ok {
		writeErrJSON(w, http.StatusBadRequest, "節點資料源不可用")
		return
	}
	hash, err := nd.n.StakeAsBootstrap()
	if err != nil {
		writeErrJSON(w, http.StatusBadRequest, "領取失敗: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"ok": true, "tx_hash": hash, "amount": 1000,
		"message": "引導獎勵交易已提交，入塊後核發 1,000 TACm 並自動質押",
	})
}

// handleNodeRegisterAPI M149m-9：PoT 節點公鑰註冊（免費、無質押，取得時間權證出塊資格）。
func (s *Server) handleNodeRegisterAPI(w http.ResponseWriter, r *http.Request) {
	nd, ok := s.ds.(*nodeDS)
	if !ok {
		writeErrJSON(w, http.StatusBadRequest, "節點資料源不可用")
		return
	}
	hash, err := nd.n.RegisterAsPotNode()
	if err != nil {
		writeErrJSON(w, http.StatusBadRequest, "註冊失敗: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"ok": true, "tx_hash": hash,
		"message": "PoT 節點註冊交易已提交，入塊後取得時間權證出塊資格",
	})
}

// handleNodeStakeAPI M149m：節點鏈上質押成為驗證人（from=節點地址、to=質押池）。
func (s *Server) handleNodeStakeAPI(w http.ResponseWriter, r *http.Request) {
	nd, ok := s.ds.(*nodeDS)
	if !ok {
		writeErrJSON(w, http.StatusBadRequest, "節點資料源不可用")
		return
	}
	var req struct {
		Amount float64 `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrJSON(w, http.StatusBadRequest, "JSON 格式錯誤")
		return
	}
	if req.Amount <= 0 {
		writeErrJSON(w, http.StatusBadRequest, "質押金額必須為正")
		return
	}
	hash, err := nd.n.StakeAsValidator(req.Amount)
	if err != nil {
		writeErrJSON(w, http.StatusBadRequest, "質押失敗: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"ok": true, "tx_hash": hash, "amount": req.Amount,
		"message": "質押交易已提交，入塊後最多 1 分鐘進入驗證人集",
	})
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
