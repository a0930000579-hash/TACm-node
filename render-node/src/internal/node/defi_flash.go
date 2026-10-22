package node

// defi_flash.go — 閃電貸（Flash Loan）RPC：POST /api/defi/flash_loan。
// 流程（單請求內原子）：借貸池（defi_lending）借出 →（可選 target 合約 callback）→
// 同請求償還（本金＋費 0.09%）→ 任一步失敗即整體回滾（借出返還、無淨變動）。
// 對標 Aave flashLoan／ERC-3156：無抵押、單筆交易完成償還，費率進池獎勵存戶。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"tacm/internal/defi"
	"tacm/internal/wallet"
)

// flashLoanFeeRate 閃電貸手續費率（0.09%，Aave 標準）。
const flashLoanFeeRate = 0.0009

// handleDefiFlashLoan POST /api/defi/flash_loan {address, asset, amount, target?, calldata?}
func (s *RPCServer) handleDefiFlashLoan(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.defiSvc()
	if !ok || s.node == nil || s.node.walletSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "DeFi 未啟用")
		return
	}
	var req struct {
		Address  string  `json:"address"`
		Asset    string  `json:"asset"`
		Amount   float64 `json:"amount"`
		Target   string  `json:"target"`
		Calldata string  `json:"calldata"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	req.Address = strings.TrimSpace(req.Address)
	req.Asset = normalizeAsset(req.Asset)
	req.Target = strings.TrimSpace(req.Target)
	if req.Address == "" || req.Amount <= 0 {
		writeErr(w, http.StatusBadRequest, "缺少地址或金額無效")
		return
	}
	market, err := ds.GetMarket(req.Asset)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if market == nil || !market.Enabled {
		writeErr(w, http.StatusBadRequest, "借貸市場不存在或未啟用")
		return
	}
	// 借貸池可用流動性檢查（存款−借款）。
	if avail := market.TotalDeposit - market.TotalBorrow; avail < req.Amount {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("借貸池流動性不足（可用 %.2f）", avail))
		return
	}
	// 借貸池帳戶實際資金檢查。
	poolAcc, _ := s.node.walletSvc.Balance(defi.LendingAccount)
	if err := defiCheckBalance(poolAcc, req.Asset, req.Amount); err != nil {
		writeErr(w, http.StatusBadRequest, "借貸池資金不足："+err.Error())
		return
	}

	fee := req.Amount * flashLoanFeeRate
	lent := false
	repayStarted := false
	rollback := func() {
		// 僅在「已借出、本金未償還」時返還借出款，保證失敗路徑無淨變動。
		if lent && !repayStarted {
			_ = s.node.walletSvc.InternalTransfer(req.Address, defi.LendingAccount,
				wallet.Asset(req.Asset), amountOf(req.Amount, req.Asset), "defi:flash:rollback")
		}
	}
	defer rollback()

	// 1) 借出（池 → 借款人）。
	if err := s.node.walletSvc.InternalTransfer(defi.LendingAccount, req.Address,
		wallet.Asset(req.Asset), amountOf(req.Amount, req.Asset), "defi:flash:lend"); err != nil {
		writeErr(w, http.StatusInternalServerError, "閃電貸借出失敗："+err.Error())
		return
	}
	lent = true

	// 2) 可選 callback：借款人指定合約使用借款（只讀模擬執行＋回傳驗證）。
	var returnData string
	if req.Target != "" {
		toTx0, terr := s.node.normalizeEthAddr(req.Target)
		if terr != nil {
			writeErr(w, http.StatusBadRequest, "target 地址無效")
			return
		}
		to0x, herr := tx0To0x(toTx0)
		if herr != nil {
			writeErr(w, http.StatusBadRequest, "target 地址轉換失敗")
			return
		}
		callerTx0, cerr := s.node.normalizeEthAddr(req.Address)
		if cerr != nil {
			writeErr(w, http.StatusBadRequest, "address 地址無效")
			return
		}
		caller0x, herr := tx0To0x(callerTx0)
		if herr != nil {
			writeErr(w, http.StatusBadRequest, "address 地址轉換失敗")
			return
		}
		calldata, derr := hexInput(req.Calldata)
		if derr != nil {
			writeErr(w, http.StatusBadRequest, "calldata hex 錯誤")
			return
		}
		cr, xerr := s.node.contracts.SimulateCallGasOverrides(to0x, caller0x, calldata, nil,
			s.node.contracts.DefaultGas(), nil, false)
		if xerr != nil || cr == nil || !cr.OK || cr.Reverted {
			writeErr(w, http.StatusInternalServerError, "閃電貸 callback 執行失敗")
			return
		}
		returnData = cr.ReturnData
	}

	// 3) 償還檢查（本金＋費；借款已入帳，餘額不足即回滾）。
	bal, _ := s.node.walletSvc.Balance(req.Address)
	if err := defiCheckBalance(bal, req.Asset, req.Amount+fee); err != nil {
		writeErr(w, http.StatusBadRequest, "償還資金不足："+err.Error())
		return
	}

	// 4) 償還本金（借款人 → 池）。
	if err := s.node.walletSvc.InternalTransfer(req.Address, defi.LendingAccount,
		wallet.Asset(req.Asset), amountOf(req.Amount, req.Asset), "defi:flash:repay"); err != nil {
		writeErr(w, http.StatusInternalServerError, "償還本金失敗："+err.Error())
		return
	}
	repayStarted = true

	// 5) 償還手續費（借款人 → 池）；失敗則退回本金（淨效果＝交易未發生）。
	if fee > 0 {
		if err := s.node.walletSvc.InternalTransfer(req.Address, defi.LendingAccount,
			wallet.Asset(req.Asset), amountOf(fee, req.Asset), "defi:flash:fee"); err != nil {
			_ = s.node.walletSvc.InternalTransfer(defi.LendingAccount, req.Address,
				wallet.Asset(req.Asset), amountOf(req.Amount, req.Asset), "defi:flash:rollback-principal")
			writeErr(w, http.StatusInternalServerError, "償還手續費失敗："+err.Error())
			return
		}
	}

	// 6) 記錄。
	if err := ds.RecordFlashLoan(req.Address, req.Asset, req.Amount, fee); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "asset": req.Asset, "amount": req.Amount, "fee": fee,
		"return_data": returnData, "message": "閃電貸完成（同交易償還）",
	})
}
