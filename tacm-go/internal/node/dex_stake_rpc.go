package node

import (
	"encoding/json"
	"log"
	"math/big"
	"net/http"
)

// ---------- 流動性挖礦（LP 質押＋鏈上獎勵）RPC ----------

type stakeReq struct {
	Pool           string `json:"pool"`
	RewardToken    string `json:"reward_token"`
	RewardPerBlock string `json:"reward_per_block"`
	Amount         string `json:"amount"`
	Shares         string `json:"shares"`
	Owner          string `json:"owner"`
}

// stakePoolJSON 農場列表項（含可選 owner 的 shares/pending/debt）。
type stakePoolJSON struct {
	PoolID         string `json:"pool_id"`
	RewardToken    string `json:"reward_token"`
	RewardPerBlock string `json:"reward_per_block"`
	TotalStaked    string `json:"total_staked"`
	AccPerShare    string `json:"acc_per_share"`
	Funded         string `json:"funded"`
	CreatedAt      int64  `json:"created_at"`
	// 可選（帶 owner 參數時回傳）。
	MyShares string `json:"my_shares,omitempty"`
	MyDebt   string `json:"my_debt,omitempty"`
	Pending  string `json:"pending,omitempty"`
}

// handleDexStakeCreate POST /dex/stake/create — 建立農場（每池一個）。
func (s *RPCServer) handleDexStakeCreate(w http.ResponseWriter, r *http.Request) {
	var req stakeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.node.dex == nil {
		writeErr(w, http.StatusBadRequest, "DEX 未啟用")
		return
	}
	if req.Pool == "" || req.RewardToken == "" {
		writeErr(w, http.StatusBadRequest, "缺少池或獎勵代幣")
		return
	}
	rpb, ok := new(big.Int).SetString(req.RewardPerBlock, 10)
	if !ok || rpb == nil || rpb.Sign() <= 0 {
		writeErr(w, http.StatusBadRequest, "每塊獎勵無效")
		return
	}
	// 驗證獎勵代幣為已發行標準代幣。
	if !s.node.contractExists(req.RewardToken) {
		writeErr(w, http.StatusBadRequest, "獎勵代幣未發行")
		return
	}
	sp, err := s.node.dex.CreateStake(req.Pool, req.RewardToken, rpb)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "stake": stakePoolJSON{
		PoolID: sp.PoolID, RewardToken: sp.RewardToken,
		RewardPerBlock: sp.RewardPerBlock.String(), TotalStaked: sp.TotalStaked.String(),
		AccPerShare: sp.AccPerShare.String(), Funded: sp.Funded.String(), CreatedAt: sp.CreatedAt,
	}})
}

// handleDexStakeFund POST /dex/stake/fund — 做市帳戶注資獎勵代幣至農場金庫。
func (s *RPCServer) handleDexStakeFund(w http.ResponseWriter, r *http.Request) {
	var req stakeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.node.dex == nil {
		writeErr(w, http.StatusBadRequest, "DEX 未啟用")
		return
	}
	amt, ok := new(big.Int).SetString(req.Amount, 10)
	if !ok || amt == nil || amt.Sign() <= 0 {
		writeErr(w, http.StatusBadRequest, "注資金額無效")
		return
	}
	sp, err := s.node.dex.FundStake(req.Pool, amt)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "stake": stakePoolJSON{
		PoolID: sp.PoolID, RewardToken: sp.RewardToken,
		RewardPerBlock: sp.RewardPerBlock.String(), TotalStaked: sp.TotalStaked.String(),
		AccPerShare: sp.AccPerShare.String(), Funded: sp.Funded.String(), CreatedAt: sp.CreatedAt,
	}})
}

// handleDexStake POST /dex/stake — 質押 LP 份額。
func (s *RPCServer) handleDexStake(w http.ResponseWriter, r *http.Request) {
	var req stakeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.node.dex == nil {
		writeErr(w, http.StatusBadRequest, "DEX 未啟用")
		return
	}
	shares, ok := new(big.Int).SetString(req.Shares, 10)
	if !ok || shares == nil || shares.Sign() <= 0 {
		writeErr(w, http.StatusBadRequest, "質押數量無效")
		return
	}
	spos, err := s.node.dex.Stake(req.Pool, s.node.nodeAddress, shares)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "position": map[string]any{
		"pool": spos.PoolID, "owner": spos.Owner, "shares": spos.Shares.String(), "debt": spos.Debt.String(),
	}})
}

// handleDexStakeUnstake POST /dex/stake/unstake — 解除質押。
func (s *RPCServer) handleDexStakeUnstake(w http.ResponseWriter, r *http.Request) {
	var req stakeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.node.dex == nil {
		writeErr(w, http.StatusBadRequest, "DEX 未啟用")
		return
	}
	shares, ok := new(big.Int).SetString(req.Shares, 10)
	if !ok || shares == nil || shares.Sign() <= 0 {
		writeErr(w, http.StatusBadRequest, "解除數量無效")
		return
	}
	spos, err := s.node.dex.Unstake(req.Pool, s.node.nodeAddress, shares)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "position": map[string]any{
		"pool": spos.PoolID, "owner": spos.Owner, "shares": spos.Shares.String(), "debt": spos.Debt.String(),
	}})
}

// handleDexStakeClaim POST /dex/stake/claim — 領取獎勵（金庫鏈上轉帳）。
func (s *RPCServer) handleDexStakeClaim(w http.ResponseWriter, r *http.Request) {
	var req stakeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.node.dex == nil {
		writeErr(w, http.StatusBadRequest, "DEX 未啟用")
		return
	}
	claimed, err := s.node.dex.Claim(req.Pool, s.node.nodeAddress)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "amount": claimed.String(), "pool": req.Pool})
}

// handleDexStakePools GET /dex/stake/pools?owner= — 農場列表（可帶 owner 持倉）。
func (s *RPCServer) handleDexStakePools(w http.ResponseWriter, r *http.Request) {
	if s.node.dex == nil {
		writeErr(w, http.StatusBadRequest, "DEX 未啟用")
		return
	}
	owner := r.URL.Query().Get("owner")
	if owner == "" {
		owner = s.node.nodeAddress
	}
	pools, err := s.node.dex.StakePools()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]stakePoolJSON, 0, len(pools))
	for _, sp := range pools {
		item := stakePoolJSON{
			PoolID: sp.PoolID, RewardToken: sp.RewardToken,
			RewardPerBlock: sp.RewardPerBlock.String(), TotalStaked: sp.TotalStaked.String(),
			AccPerShare: sp.AccPerShare.String(), Funded: sp.Funded.String(), CreatedAt: sp.CreatedAt,
		}
		spos, err := s.node.dex.StakePositionOf(sp.PoolID, owner)
		if err == nil {
			item.MyShares = spos.Shares.String()
			item.MyDebt = spos.Debt.String()
		}
		if p, err := s.node.dex.Pending(sp.PoolID, owner); err == nil {
			item.Pending = p.String()
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pools": out})
}

// handleDexStakePending GET /dex/stake/pending?pool=&owner= — 未領取獎勵。
func (s *RPCServer) handleDexStakePending(w http.ResponseWriter, r *http.Request) {
	if s.node.dex == nil {
		writeErr(w, http.StatusBadRequest, "DEX 未啟用")
		return
	}
	pool := r.URL.Query().Get("pool")
	owner := r.URL.Query().Get("owner")
	if owner == "" {
		owner = s.node.nodeAddress
	}
	p, err := s.node.dex.Pending(pool, owner)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pending": p.String(), "pool": pool, "owner": owner})
}

// contractExists 檢查獎勵代幣是否為已發行標準代幣（讀 VM 合約清單）。
func (n *Node) contractExists(address string) bool {
	if n.contracts == nil {
		return false
	}
	addr0x, err := tx0To0x(address)
	if err != nil {
		return false
	}
	info := n.contracts.Get(addr0x)
	return info != nil && info.CodeSize > 0
}

// dexStakeTick 於新塊入庫後推進農場獎勵累計（由 produceBlock 呼叫）。
func (n *Node) dexStakeTick(height int64) {
	if n.dex == nil {
		return
	}
	if err := n.dex.StakeTickTry(height); err != nil {
		// 出塊主流程不得因農場 tick 失敗而回滾；記錄即可。
		log.Printf("[dex-stake] tick h=%d: %v", height, err)
	}
}
