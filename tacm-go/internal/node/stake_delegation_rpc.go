package node

// M156 質押委託（Staking Delegation）RPC：
//   POST /stake/delegate   {from, validator, amount}  委託（站內帳本鎖倉＋鏈上紀錄）
//   POST /stake/undelegate {from, validator, amount}  撤銷（站內帳本返還＋鏈上紀錄）
//   GET  /stake/status?address=                       我的委託＋全驗證人委託總額
// 委託交易由節點金鑰代簽上鏈（from=節點、memo=pos:delegate:<delegator>:<validator>），
// 任何節點同步同鏈即得到一致的委託紀錄（delegations 表重放）。

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"tacm/internal/chaindb"
	"tacm/internal/crypto"
	"tacm/internal/wallet"
)

const stakePoolAddress = "pos_stake_pool"

type delegationReq struct {
	From      string `json:"from"`
	Validator string `json:"validator"`
	Amount    string `json:"amount"`
}

// submitDelegationTx 以節點金鑰代簽委託/撤銷交易上鏈（from=節點、memo 含委託人與驗證人）。
func (n *Node) submitDelegationTx(delegator, validator, amount string, undelegate bool) (string, error) {
	if n.keypair == nil {
		return "", errors.New("node: 節點金鑰不可用")
	}
	if !crypto.IsValidAddress(delegator) || !crypto.IsValidAddress(validator) {
		return "", errors.New("node: 委託人或驗證人地址非法")
	}
	amt, err := strconv.ParseFloat(amount, 64)
	if err != nil || amt <= 0 {
		return "", errors.New("node: 委託金額必須為正數")
	}
	prefix := chaindb.DelegationMemo
	if undelegate {
		prefix = chaindb.UndelegationMemo
	}
	from, err := n.keypair.Address()
	if err != nil {
		return "", err
	}
	nonce := n.db.GetNonce(from) + n.pendingTxCount(from)
	tx := map[string]any{
		"from": from, "to": stakePoolAddress,
		"amount": strconv.FormatFloat(amt, 'f', 8, 64), "fee": "0",
		"nonce": nonce, "ts": time.Now().Unix(),
		"pubkey": hex.EncodeToString(n.keypair.PublicKeyCompressed()),
		"memo":   fmt.Sprintf("%s:%s:%s", prefix, delegator, validator),
	}
	sig, err := crypto.SignTransaction(tx, n.keypair.PrivateKey())
	if err != nil {
		return "", fmt.Errorf("node: 簽名委託交易: %w", err)
	}
	tx["signature"] = sig
	return n.SubmitTransaction(tx)
}

// handleStakeDelegate POST /stake/delegate {from,validator,amount}
// ①站內帳本鎖倉（from → stake pool）②代簽委託交易上鏈。
func (s *RPCServer) handleStakeDelegate(w http.ResponseWriter, r *http.Request) {
	if s.node.walletSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "wallet service disabled")
		return
	}
	var req delegationReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	if !crypto.IsValidAddress(req.From) || !crypto.IsValidAddress(req.Validator) {
		writeErr(w, http.StatusBadRequest, "invalid address")
		return
	}
	amount, err := wallet.NewAmount(req.Amount, wallet.AssetTACm)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// ①站內帳本鎖倉：委託人 TACm → 質押池（返還憑鏈上撤銷紀錄）。
	if _, err := s.node.walletSvc.Transfer(req.From, stakePoolAddress, wallet.AssetTACm, amount, "stake delegate"); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// ②鏈上委託紀錄（節點代簽）。
	txHash, err := s.node.submitDelegationTx(req.From, req.Validator, req.Amount, false)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "delegator": req.From, "validator": req.Validator,
		"amount": req.Amount, "tx_hash": txHash,
	})
}

// handleStakeUndelegate POST /stake/undelegate {from,validator,amount}
// ①代簽撤銷交易上鏈 ②站內帳本返還（stake pool → from）。
func (s *RPCServer) handleStakeUndelegate(w http.ResponseWriter, r *http.Request) {
	if s.node.walletSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "wallet service disabled")
		return
	}
	var req delegationReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	if !crypto.IsValidAddress(req.From) || !crypto.IsValidAddress(req.Validator) {
		writeErr(w, http.StatusBadRequest, "invalid address")
		return
	}
	amount, err := wallet.NewAmount(req.Amount, wallet.AssetTACm)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = amount // M159：金額僅做格式驗證；返還改由鏈上解綁期滿自動完成。
	// ①鏈上撤銷紀錄（節點代簽；重放時驗證委託額度，超額會回滾）。
	txHash, err := s.node.submitDelegationTx(req.From, req.Validator, req.Amount, true)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// M159：委託解綁定時限——資產進入解綁佇列，鎖倉 UnbondingPeriod 區塊後
	// 由節點自動返還（pos:unbond_pay 鏈上入帳），此處不再即時返還。
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "delegator": req.From, "validator": req.Validator,
		"amount": req.Amount, "tx_hash": txHash,
		"unbonding_period": chaindb.UnbondingPeriod,
		"note": "assets unlock after unbonding period (auto-refund on-chain)",
	})
}

// handleStakeStatus GET /stake/status?address=
// 回傳：我的委託（delegator 視角）＋全驗證人委託總額（validator 視角）。
func (s *RPCServer) handleStakeStatus(w http.ResponseWriter, r *http.Request) {
	addr := r.URL.Query().Get("address")
	var myDelegations []chaindb.Delegation
	if addr != "" {
		if !crypto.IsValidAddress(addr) {
			writeErr(w, http.StatusBadRequest, "invalid address")
			return
		}
		dels, err := s.node.db.GetDelegations(addr)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		myDelegations = dels
	}
	// 全驗證人委託總額：遍歷驗證人取委託。
	validators, err := s.node.db.GetPosValidators()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	type valDeleg struct {
		Validator string  `json:"validator"`
		Total     float64 `json:"total"`
		Count     int     `json:"count"`
		Status    string  `json:"status"`
		Stake     float64 `json:"total_stake"`
	}
	var vd []valDeleg
	for _, v := range validators {
		dels, err := s.node.db.GetValidatorDelegations(v.Address)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		total := 0.0
		for _, d := range dels {
			total += d.Amount
		}
		if len(dels) > 0 {
			vd = append(vd, valDeleg{Validator: v.Address, Total: total, Count: len(dels), Status: v.Status, Stake: v.TotalStake})
		}
	}
	// M157：委託者累計委託獎勵（鏈上 coinbase:deleg 入賬）。
	delegReward := 0.0
	rewardTxs := int64(0)
	if addr != "" {
		if rw, err := s.node.db.GetDelegationRewardTotal(addr); err == nil {
			delegReward = rw
		}
		if rc, err := s.node.db.CountDelegationRewards(); err == nil {
			rewardTxs = rc
		}
	}
	// M159：解綁中紀錄（pending 佇列）。
	var unbondings []chaindb.Unbonding
	if addr != "" {
		if ubs, err := s.node.db.GetUnbondings(addr); err == nil {
			unbondings = ubs
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "address": addr,
		"my_delegations":        myDelegations,
		"validator_delegations": vd,
		"deleg_rewards":         delegReward,
		"deleg_reward_txs":      rewardTxs,
		"unbondings":            unbondings,
		"unbonding_period":      chaindb.UnbondingPeriod,
	})
}
