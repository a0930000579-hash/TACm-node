package node

import (
	"encoding/json"
	"fmt"
	"net/http"

	"tacm/internal/exchange"
	"tacm/internal/wallet"
)

// toWalletAmt 將 exchange.Amount 轉為 wallet.Amount（結構相同：Big×TACm / I64×穩定幣）。
func toWalletAmt(a exchange.Amount) wallet.Amount {
	if a.Big != nil {
		return wallet.Amount{Big: a.Big}
	}
	return wallet.Amount{I64: a.I64}
}

// ExchangeVault 交易所託管地址（鏈上側收款/付款帳戶）。
// 入金：用戶錢包 → vault（鏈上帳本留痕）→ 交易所餘額入帳。
// 提現：交易所餘額扣減 → vault → 用戶錢包。
const ExchangeVault = "exchange_vault"

// handleExchangeDepositFromWallet POST /api/exchange/deposit_from_wallet {uid,asset,amount}
// 鏈上錢包 → 交易所：扣鏈上錢包（含鏈上轉帳費）後 credit 交易所餘額；失敗回滾。
func (s *RPCServer) handleExchangeDepositFromWallet(w http.ResponseWriter, r *http.Request) {
	if s.node.walletSvc == nil || s.node.exchangeSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "wallet_or_exchange_disabled"})
		return
	}
	var req struct {
		UID    string `json:"uid"`
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
	amt := mustAmt(req.Amount, a)
	if req.UID == "" || amt.IsZero() {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "uid_or_amount_required"})
		return
	}
	wa, wok := wallet.NormalizeAsset(req.Asset)
	if !wok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "wallet 不支持的資產: " + req.Asset})
		return
	}
	// 1) 鏈上錢包轉至 vault（扣轉帳費，fee 入錢包 fee 帳戶）。
	wfee, err := s.node.walletSvc.Transfer(req.UID, ExchangeVault, wa, toWalletAmt(amt), "exchange deposit")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "wallet 扣款失敗: " + err.Error()})
		return
	}
	// 2) 交易所入帳；失敗回滾鏈上。
	if err := s.node.exchangeSvc.Deposit(req.UID, a, amt, "from wallet"); err != nil {
		if _, rbErr := s.node.walletSvc.Transfer(ExchangeVault, req.UID, wa, toWalletAmt(amt), "rollback deposit"); rbErr == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "exchange 入帳失敗（已回滾）: " + err.Error()})
		} else {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "exchange 入帳失敗且回滾異常: " + rbErr.Error()})
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "uid": req.UID, "asset": req.Asset, "amount": req.Amount,
		"chain_fee": wfee.String(wallet.Asset(a)),
	})
}

// handleExchangeWithdrawToWallet POST /api/exchange/withdraw_to_wallet {uid,asset,amount}
// 交易所 → 鏈上錢包：扣交易所餘額後由 vault 轉至用戶錢包（扣鏈上轉帳費）；失敗回滾。
func (s *RPCServer) handleExchangeWithdrawToWallet(w http.ResponseWriter, r *http.Request) {
	if s.node.walletSvc == nil || s.node.exchangeSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "wallet_or_exchange_disabled"})
		return
	}
	var req struct {
		UID    string `json:"uid"`
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
	amt := mustAmt(req.Amount, a)
	if req.UID == "" || amt.IsZero() {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "uid_or_amount_required"})
		return
	}
	wa, wok := wallet.NormalizeAsset(req.Asset)
	if !wok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "wallet 不支持的資產: " + req.Asset})
		return
	}
	// 1) 扣交易所餘額。
	if err := s.node.exchangeSvc.Withdraw(req.UID, a, amt); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "exchange 扣款失敗: " + err.Error()})
		return
	}
	// 2) vault → 用戶錢包；失敗回滾交易所。
	wfee, err := s.node.walletSvc.Transfer(ExchangeVault, req.UID, wa, toWalletAmt(amt), "exchange withdraw")
	if err != nil {
		if rbErr := s.node.exchangeSvc.Deposit(req.UID, a, amt, "rollback withdraw"); rbErr == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "鏈上轉帳失敗（已回滾）: " + err.Error()})
		} else {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "鏈上轉帳失敗且回滾異常: " + rbErr.Error()})
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "uid": req.UID, "asset": req.Asset, "amount": req.Amount,
		"chain_fee": wfee.String(wallet.Asset(a)),
	})
}

// handleExchangeFeeAccount GET /api/exchange/fee — 手續費帳戶餘額。
func (s *RPCServer) handleExchangeFeeAccount(w http.ResponseWriter, r *http.Request) {
	if s.node.exchangeSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "exchange_disabled"})
		return
	}
	bal, err := s.node.exchangeSvc.Balances(exchange.FeeUID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	out := []map[string]any{}
	for _, b := range bal {
		out = append(out, map[string]any{"asset": string(b.Asset), "avail": b.AvailStr, "locked": b.LockedStr})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "fee_account": out})
}

var _ = fmt.Sprintf // 保留 fmt 於本檔案
