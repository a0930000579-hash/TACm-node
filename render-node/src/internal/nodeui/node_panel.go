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
	"time"

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
	rs := s.n.DB().NodeRewardStats(addr, 200)
	v["reward_count"] = rs.RewardCount
	v["reward_total"] = rs.RewardTotal
	v["last_reward_block"] = rs.LastRewardBlock
	v["heartbeat_count"] = rs.HeartbeatCount
	v["last_heartbeat"] = rs.LastHeartbeat
	if v["node_id"] == "" {
		v["node_id"] = s.nodeID
	}
	// v53-B2/C4：共識診斷（follower 的 finalized 0 為真實 2/3 共識現狀，附說明）
	// + P2P 對等節點（admin_peers 資料源，與主鏈 admin_peers 同構）。
	fh := s.n.FinalizedHeight()
	vs := s.n.ValidatorSet()
	v["finalized_height"] = fh
	v["validator_total"] = vs.Len()
	v["validator_power"] = vs.TotalPower()
	v["validator_quorum"] = vs.QuorumPower()
	v["consensus_note"] = ""
	if fh <= 0 {
		v["consensus_note"] = "尚未達成 BFT 2/3 最終化：最終化需 >2/3 驗證人對同一區塊提交 precommit；單/雙驗證人網路屬正常狀態，不影響出塊與同步"
	}
	peers := []map[string]any{}
	if pn := s.n.P2PNet(); pn != nil {
		for _, p := range pn.PeerSnapshot() {
			peers = append(peers, map[string]any{
				"id": p.NodeID, "base_url": p.BaseURL, "owner": p.Owner,
				"height": p.Height, "finalized_height": p.Finalized,
				"online": p.Online, "last_seen": p.LastSeen.Format(time.RFC3339),
			})
		}
	}
	v["peers"] = peers
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


// handleNodeStakeAPI M149m：質押成為驗證人（鏈上質押交易 pos:stake → pos_stake_pool）。
// handleNodeRegisterAPI M149m-9：PoT 節點公鑰註冊（免費、無質押）。
func (s *Server) handleNodeRegisterAPI(w http.ResponseWriter, r *http.Request) {
	hash, err := s.n.RegisterAsPotNode()
	if err != nil {
		writeErrJSON(w, http.StatusBadRequest, "註冊失敗: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"ok": true, "tx_hash": hash,
		"message": "PoT 節點註冊交易已提交，入塊後取得時間權證出塊資格",
	})
}

func (s *Server) handleNodeStakeAPI(w http.ResponseWriter, r *http.Request) {
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
	hash, err := s.n.StakeAsValidator(req.Amount)
	if err != nil {
		writeErrJSON(w, http.StatusBadRequest, "質押失敗: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"ok": true, "tx_hash": hash, "amount": req.Amount,
		"message": "質押交易已提交，入塊後最多 1 分鐘進入驗證人集",
	})
}

// handleNodeBootstrapAPI M149m-2：新節點領取引導獎勵（創世社群基金核發 1,000 並自動質押）。
func (s *Server) handleNodeBootstrapAPI(w http.ResponseWriter, r *http.Request) {
	hash, err := s.n.StakeAsBootstrap()
	if err != nil {
		writeErrJSON(w, http.StatusBadRequest, "領取失敗: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"ok": true, "tx_hash": hash, "amount": 1000,
		"message": "引導獎勵交易已提交，入塊後核發 1,000 TACm 並自動質押",
	})
}

func writeErrJSON(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": msg})
}
