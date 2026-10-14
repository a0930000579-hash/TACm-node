package node

import (
	"encoding/json"
	"fmt"
	"net/http"

	"tacm/internal/exchange"
	"tacm/internal/wallet"
)

// handleRewardPool GET /api/rewardpool — 鏈上獎勵池餘額與挹注資訊。
func (s *RPCServer) handleRewardPool(w http.ResponseWriter, r *http.Request) {
	if s.node.walletSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "wallet_disabled"})
		return
	}
	acct, err := s.node.walletSvc.Balance(wallet.RewardPoolAddr)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"pool":     wallet.RewardPoolAddr,
		"tacm":     acct.TACmBalance.String(),
		"share_bp": wallet.PoolShareBps,
		"note":     "每區塊 coinbase 按 share_bp 挹注；交易所手續費可經 /api/rewardpool/topup 結算入池",
	})
}

// handleExchangePool GET /api/exchange/pool — 交易所資金池視圖（獎勵池作為交易所資金用途）。
func (s *RPCServer) handleExchangePool(w http.ResponseWriter, r *http.Request) {
	if s.node.walletSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "wallet_disabled"})
		return
	}
	sum, err := s.node.walletSvc.Store().PoolSummary()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	assets := []map[string]any{}
	for _, a := range sum.Assets {
		assets = append(assets, map[string]any{
			"asset": a.Asset, "balance": a.Balance,
			"total_injected": a.TotalInjected, "total_claimed": a.TotalClaimed, "count": a.Count,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"pool":     wallet.RewardPoolAddr,
		"assets":   assets,
		"share_bp": wallet.PoolShareBps,
		"note":     "獎勵池＝交易所資金池：每區塊 coinbase 12% 自動挹注＋交易所手續費結算；資金作交易所運營/流動性用途",
	})
}

// handleRewardPoolTopup POST /api/rewardpool/topup {asset, amount}
// 把交易所手續費帳戶（fee）資產結算入鏈上獎勵池；失敗回滾。
func (s *RPCServer) handleRewardPoolTopup(w http.ResponseWriter, r *http.Request) {
	if s.node.walletSvc == nil || s.node.exchangeSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "wallet_or_exchange_disabled"})
		return
	}
	var req struct {
		Asset  string `json:"asset"`
		Amount string `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_json"})
		return
	}
	a, aok := exchange.NormAssetEx(req.Asset)
	if !aok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "不支持的資產: " + req.Asset})
		return
	}
	wa, wok := wallet.NormalizeAsset(req.Asset)
	if !wok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "wallet 不支持的資產: " + req.Asset})
		return
	}
	amt := mustAmt(req.Amount, a)
	if amt.IsZero() {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "amount_required"})
		return
	}
	// 1) 結算交易所 fee 帳戶（專用 DrainFee，僅允許 fee 帳戶）。
	if err := s.node.exchangeSvc.DrainFee(a, amt); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "交易所 fee 帳戶結算失敗: " + err.Error()})
		return
	}
	// 2) 挹注鏈上獎勵池；失敗回滾。
	if err := s.node.walletSvc.Deposit(wallet.RewardPoolAddr, wa, toWalletAmt(amt), "exchange fee topup"); err != nil {
		if rbErr := s.node.exchangeSvc.Deposit(exchange.FeeUID, a, amt, "rollback topup"); rbErr == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "獎勵池入帳失敗（已回滾）: " + err.Error()})
		} else {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "獎勵池入帳失敗且回滾異常: " + rbErr.Error()})
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "asset": req.Asset, "amount": req.Amount, "pool": wallet.RewardPoolAddr})
}

// handleRewardPoolWithdraw POST /api/rewardpool/withdraw {to, amount}
// 獎勵池 → 錢包地址（站內轉帳，轉帳費由池承擔）。
func (s *RPCServer) handleRewardPoolWithdraw(w http.ResponseWriter, r *http.Request) {
	if s.node.walletSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "wallet_disabled"})
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
	amt, err := wallet.NewAmount(req.Amount, wallet.AssetTACm)
	if err != nil || req.To == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "to_or_amount_invalid"})
		return
	}
	fee, err := s.node.walletSvc.Transfer(wallet.RewardPoolAddr, req.To, wallet.AssetTACm, amt, "reward pool withdraw")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "獎勵池轉出失敗: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "to": req.To, "amount": req.Amount, "fee": fee.String(wallet.AssetTACm)})
}

var _ = fmt.Sprintf
