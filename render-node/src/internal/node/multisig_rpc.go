package node

// v129：鏈上多簽錢包 RPC（Gnosis Safe 模式）。
// create → submit → confirm（達 threshold）→ execute（實際錢包轉帳）。

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"tacm/internal/wallet"
)

// handleMultisigCreate POST /api/multisig/create {address, owners[], threshold}
func (s *RPCServer) handleMultisigCreate(w http.ResponseWriter, r *http.Request) {
	ms := s.node.Multisig()
	if ms == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "multisig_disabled"})
		return
	}
	var req struct {
		Address   string   `json:"address"`
		Owners    []string `json:"owners"`
		Threshold int      `json:"threshold"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_json"})
		return
	}
	req.Address = strings.TrimSpace(req.Address)
	if !validTACAddress(req.Address) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_address"})
		return
	}
	for _, o := range req.Owners {
		if !validTACAddress(strings.TrimSpace(o)) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_owner_address"})
			return
		}
	}
	tip := s.node.DB().GetTipHeight()
	if err := ms.CreateWallet(req.Address, req.Owners, req.Threshold, tip, ""); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "address": req.Address, "owners": req.Owners, "threshold": req.Threshold})
}

// handleMultisigList GET /api/multisig/list
func (s *RPCServer) handleMultisigList(w http.ResponseWriter, r *http.Request) {
	ms := s.node.Multisig()
	if ms == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "multisig_disabled"})
		return
	}
	list, err := ms.Wallets()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "wallets": list})
}

// handleMultisigInfo GET /api/multisig/info?address=0x...
func (s *RPCServer) handleMultisigInfo(w http.ResponseWriter, r *http.Request) {
	ms := s.node.Multisig()
	if ms == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "multisig_disabled"})
		return
	}
	addr := strings.TrimSpace(r.URL.Query().Get("address"))
	if !validTACAddress(addr) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_address"})
		return
	}
	wlt, err := ms.WalletInfo(addr)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if wlt == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "wallet_not_found"})
		return
	}
	txs, err := ms.Txs(addr)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "wallet": wlt, "txs": txs})
}

// handleMultisigSubmit POST /api/multisig/submit {wallet, to, asset, amount, memo, by}
func (s *RPCServer) handleMultisigSubmit(w http.ResponseWriter, r *http.Request) {
	ms := s.node.Multisig()
	if ms == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "multisig_disabled"})
		return
	}
	var req struct {
		Wallet string `json:"wallet"`
		To     string `json:"to"`
		Asset  string `json:"asset"`
		Amount string `json:"amount"`
		Memo   string `json:"memo"`
		By     string `json:"by"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_json"})
		return
	}
	req.Wallet = strings.TrimSpace(req.Wallet)
	req.To = strings.TrimSpace(req.To)
	req.By = strings.TrimSpace(req.By)
	req.Asset = strings.TrimSpace(req.Asset)
	if req.Asset == "" {
		req.Asset = "TACm"
	}
	if !validTACAddress(req.Wallet) || !validTACAddress(req.To) || !validTACAddress(req.By) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_address"})
		return
	}
	tip := s.node.DB().GetTipHeight()
	id, err := ms.SubmitTx(req.Wallet, req.To, req.Asset, req.Amount, req.Memo, req.By, tip)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tx_id": id})
}

// handleMultisigConfirm POST /api/multisig/confirm {tx_id, by}
func (s *RPCServer) handleMultisigConfirm(w http.ResponseWriter, r *http.Request) {
	ms := s.node.Multisig()
	if ms == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "multisig_disabled"})
		return
	}
	var req struct {
		TxID int64  `json:"tx_id"`
		By   string `json:"by"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_json"})
		return
	}
	req.By = strings.TrimSpace(req.By)
	if !validTACAddress(req.By) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_address"})
		return
	}
	if req.TxID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_tx_id"})
		return
	}
	tip := s.node.DB().GetTipHeight()
	n, err := ms.ConfirmTx(req.TxID, req.By, tip)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tx_id": req.TxID, "confirmations": n})
}

// handleMultisigExecute POST /api/multisig/execute {tx_id, by}
func (s *RPCServer) handleMultisigExecute(w http.ResponseWriter, r *http.Request) {
	ms := s.node.Multisig()
	if ms == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "multisig_disabled"})
		return
	}
	var req struct {
		TxID int64  `json:"tx_id"`
		By   string `json:"by"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_json"})
		return
	}
	req.By = strings.TrimSpace(req.By)
	if !validTACAddress(req.By) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_address"})
		return
	}
	if req.TxID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_tx_id"})
		return
	}
	execFn := func(wAddr, to, asset, amount, memo string) (string, error) {
		if s.node.walletSvc == nil {
			return "", errors.New("multisig: 錢包服務未啟用")
		}
		amt, err := wallet.NewAmount(amount, wallet.Asset(asset))
		if err != nil {
			return "", err
		}
		_, err = s.node.walletSvc.Transfer(wAddr, to, wallet.Asset(asset), amt, memo)
		if err != nil {
			return "", err
		}
		return "wallet:" + to + ":" + asset, nil
	}
	tx, err := ms.ExecuteTx(req.TxID, req.By, execFn)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tx": tx})
}

// handleMultisigCancel POST /api/multisig/cancel {tx_id, by}
func (s *RPCServer) handleMultisigCancel(w http.ResponseWriter, r *http.Request) {
	ms := s.node.Multisig()
	if ms == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "multisig_disabled"})
		return
	}
	var req struct {
		TxID int64  `json:"tx_id"`
		By   string `json:"by"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_json"})
		return
	}
	req.By = strings.TrimSpace(req.By)
	if !validTACAddress(req.By) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_address"})
		return
	}
	if err := ms.CancelTx(req.TxID, req.By); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tx_id": req.TxID})
}

// handleMultisigTxs GET /api/multisig/txs?address=0x...
func (s *RPCServer) handleMultisigTxs(w http.ResponseWriter, r *http.Request) {
	ms := s.node.Multisig()
	if ms == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "multisig_disabled"})
		return
	}
	addr := strings.TrimSpace(r.URL.Query().Get("address"))
	if !validTACAddress(addr) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_address"})
		return
	}
	txs, err := ms.Txs(addr)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "txs": txs})
}

// handleMultisigConfirmations GET /api/multisig/confirmations?tx_id=N
func (s *RPCServer) handleMultisigConfirmations(w http.ResponseWriter, r *http.Request) {
	ms := s.node.Multisig()
	if ms == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "multisig_disabled"})
		return
	}
	id, err := strconv.ParseInt(r.URL.Query().Get("tx_id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid tx_id"})
		return
	}
	owners, err := ms.TxConfirmations(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tx_id": id, "confirmations": owners})
}
