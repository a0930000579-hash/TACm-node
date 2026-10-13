package node

// defi_rpc.go — DeFi API（流動性挖礦＋借貸市場），資產全部對接 wallet 帳本：
//   - 入池/存款/抵押/還款：wallet.Transfer（鏈上費 2% 入資金池）
//   - 獎勵/借款發放：wallet.Reward（鑄造）／池內資金 InternalTransfer（免二次費）
//   - 池出資金：InternalTransfer（池帳戶→用戶）

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"

	"tacm/internal/defi"
	"tacm/internal/wallet"
)

// defiAddr 要求地址參數（GET query 或 POST body）。
func defiAddr(r *http.Request) (string, error) {
	var body struct {
		Address string `json:"address"`
	}
	if r.Method == http.MethodGet {
		body.Address = r.URL.Query().Get("address")
	} else {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return "", fmt.Errorf("參數解析失敗")
		}
	}
	addr := strings.TrimSpace(body.Address)
	if addr == "" {
		return "", fmt.Errorf("缺少地址")
	}
	return addr, nil
}

// defiSvc 存取器。
func (s *RPCServer) defiSvc() (*defi.Store, bool) {
	if s.node == nil || s.node.defiSvc == nil {
		return nil, false
	}
	return s.node.defiSvc, true
}

// handleDefiPools GET /api/defi/pools
func (s *RPCServer) handleDefiPools(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.defiSvc()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "DeFi 未啟用")
		return
	}
	pools, err := ds.Pools()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pools": pools})
}

// handleDefiMyLiquidity GET /api/defi/my-liquidity?address=
func (s *RPCServer) handleDefiMyLiquidity(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.defiSvc()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "DeFi 未啟用")
		return
	}
	addr, err := defiAddr(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	positions, err := ds.MyLiquidity(addr)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "positions": positions})
}

// handleDefiAddLiquidity POST /api/defi/add-liquidity {address,pool_id,amount0,amount1}
func (s *RPCServer) handleDefiAddLiquidity(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.defiSvc()
	if !ok || s.node == nil || s.node.walletSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "DeFi 未啟用")
		return
	}
	var req struct {
		Address string  `json:"address"`
		PoolID  int64   `json:"pool_id"`
		Amount0 float64 `json:"amount0"`
		Amount1 float64 `json:"amount1"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	req.Address = strings.TrimSpace(req.Address)
	if req.Address == "" || req.Amount0 <= 0 || req.Amount1 <= 0 {
		writeErr(w, http.StatusBadRequest, "缺少地址或金額無效")
		return
	}
	pool, err := ds.GetPool(req.PoolID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if pool == nil {
		writeErr(w, http.StatusNotFound, "流動性池不存在")
		return
	}
	account := defi.PoolAccount(req.PoolID)
	acc, _ := s.node.walletSvc.Balance(account)
	// 餘額檢查：兩種代幣必須充足（含費 2%）。
	if err := defiCheckBalance(acc, pool.Token0, req.Amount0); err != nil {
		writeErr(w, http.StatusBadRequest, "T0 餘額不足："+err.Error())
		return
	}
	if err := defiCheckBalance(acc, pool.Token1, req.Amount1); err != nil {
		writeErr(w, http.StatusBadRequest, "T1 餘額不足："+err.Error())
		return
	}
	if _, err := s.node.walletSvc.Transfer(req.Address, account, wallet.Asset(pool.Token0), amountOf(req.Amount0, pool.Token0), fmt.Sprintf("defi:add:pool%d:%s", req.PoolID, pool.Token0)); err != nil {
		writeErr(w, http.StatusBadRequest, "T0 轉入失敗："+err.Error())
		return
	}
	if _, err := s.node.walletSvc.Transfer(req.Address, account, wallet.Asset(pool.Token1), amountOf(req.Amount1, pool.Token1), fmt.Sprintf("defi:add:pool%d:%s", req.PoolID, pool.Token1)); err != nil {
		// 退款 T0（維持守恆）
		_ = s.node.walletSvc.InternalTransfer(account, req.Address, wallet.Asset(pool.Token0), amountOf(req.Amount0, pool.Token0), "defi:refund")
		writeErr(w, http.StatusBadRequest, "T1 轉入失敗："+err.Error())
		return
	}
	lp, err := ds.AddLiquidity(req.Address, req.PoolID, req.Amount0, req.Amount1)
	if err != nil {
		// 退款兩筆（維持守恆）
		_ = s.node.walletSvc.InternalTransfer(account, req.Address, wallet.Asset(pool.Token0), amountOf(req.Amount0, pool.Token0), "defi:refund")
		_ = s.node.walletSvc.InternalTransfer(account, req.Address, wallet.Asset(pool.Token1), amountOf(req.Amount1, pool.Token1), "defi:refund")
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "lp_amount": lp, "pool_id": req.PoolID, "message": "添加流動性成功"})
}

// handleDefiRemoveLiquidity POST /api/defi/remove-liquidity {address,position_id,lp_amount}
func (s *RPCServer) handleDefiRemoveLiquidity(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.defiSvc()
	if !ok || s.node == nil || s.node.walletSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "DeFi 未啟用")
		return
	}
	var req struct {
		Address    string  `json:"address"`
		PositionID int64   `json:"position_id"`
		LPAmount   float64 `json:"lp_amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	req.Address = strings.TrimSpace(req.Address)
	if req.Address == "" || req.LPAmount <= 0 {
		writeErr(w, http.StatusBadRequest, "缺少地址或 LP 數量無效")
		return
	}
	amount0, amount1, poolID, err := ds.RemoveLiquidity(req.Address, req.PositionID, req.LPAmount)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	account := defi.PoolAccount(poolID)
	// 池帳戶有 seed 注資＋用戶存入；轉帳失敗則回滾池/頭寸（維持守恆）。
	token0, token1 := poolTokens(poolID)
	if err := s.node.walletSvc.InternalTransfer(account, req.Address, wallet.Asset(token0), amountOf(amount0, token0), "defi:remove"); err != nil {
		_, _ = ds.AddLiquidity(req.Address, poolID, amount0, amount1) // 回滾：反加池/頭寸
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.node.walletSvc.InternalTransfer(account, req.Address, wallet.Asset(token1), amountOf(amount1, token1), "defi:remove"); err != nil {
		_, _ = ds.AddLiquidity(req.Address, poolID, amount0, amount1)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "amount0": amount0, "amount1": amount1, "message": "移除流動性成功"})
}

// handleDefiClaimReward POST /api/defi/claim-reward/{id} {address}
func (s *RPCServer) handleDefiClaimReward(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.defiSvc()
	if !ok || s.node == nil || s.node.walletSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "DeFi 未啟用")
		return
	}
	positionID, err := postIDPath(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "position id 無效")
		return
	}
	addr, err := defiAddr(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	reward, token, err := ds.ClaimLPReward(addr, positionID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.node.walletSvc.Reward(addr, wallet.Asset(token), amountOf(reward, token), fmt.Sprintf("defi:lp-reward:pos%d", positionID)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reward": reward, "token": token, "message": "領取獎勵成功"})
}

// handleDefiLendingMarkets GET /api/defi/lending-markets
func (s *RPCServer) handleDefiLendingMarkets(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.defiSvc()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "DeFi 未啟用")
		return
	}
	markets, err := ds.LendingMarkets()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "markets": markets})
}

// handleDefiMyDeposits GET /api/defi/my-deposits?address=
func (s *RPCServer) handleDefiMyDeposits(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.defiSvc()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "DeFi 未啟用")
		return
	}
	addr, err := defiAddr(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	deposits, err := ds.MyDeposits(addr)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deposits": deposits})
}

// handleDefiMyLoans GET /api/defi/my-loans?address=
func (s *RPCServer) handleDefiMyLoans(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.defiSvc()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "DeFi 未啟用")
		return
	}
	addr, err := defiAddr(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	loans, err := ds.MyLoans(addr)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "loans": loans})
}

// handleDefiDeposit POST /api/defi/deposit {address,asset,amount}
func (s *RPCServer) handleDefiDeposit(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.defiSvc()
	if !ok || s.node == nil || s.node.walletSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "DeFi 未啟用")
		return
	}
	var req struct {
		Address string  `json:"address"`
		Asset   string  `json:"asset"`
		Amount  float64 `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	req.Address = strings.TrimSpace(req.Address)
	req.Asset = normalizeAsset(req.Asset)
	if req.Address == "" || req.Amount <= 0 {
		writeErr(w, http.StatusBadRequest, "缺少地址或金額無效")
		return
	}
	if _, err := s.node.walletSvc.Transfer(req.Address, defi.LendingAccount, wallet.Asset(req.Asset), amountOf(req.Amount, req.Asset), "defi:lending:deposit"); err != nil {
		writeErr(w, http.StatusBadRequest, "存款轉帳失敗："+err.Error())
		return
	}
	if err := ds.DepositLending(req.Address, req.Asset, req.Amount); err != nil {
		_ = s.node.walletSvc.InternalTransfer(defi.LendingAccount, req.Address, wallet.Asset(req.Asset), amountOf(req.Amount, req.Asset), "defi:refund")
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "amount": req.Amount, "asset": req.Asset, "message": "存款成功"})
}

// handleDefiWithdraw POST /api/defi/withdraw {address,asset,amount}
func (s *RPCServer) handleDefiWithdraw(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.defiSvc()
	if !ok || s.node == nil || s.node.walletSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "DeFi 未啟用")
		return
	}
	var req struct {
		Address string  `json:"address"`
		Asset   string  `json:"asset"`
		Amount  float64 `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	req.Address = strings.TrimSpace(req.Address)
	req.Asset = normalizeAsset(req.Asset)
	if req.Address == "" || req.Amount <= 0 {
		writeErr(w, http.StatusBadRequest, "缺少地址或金額無效")
		return
	}
	if err := ds.WithdrawLending(req.Address, req.Asset, req.Amount); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.node.walletSvc.InternalTransfer(defi.LendingAccount, req.Address, wallet.Asset(req.Asset), amountOf(req.Amount, req.Asset), "defi:lending:withdraw"); err != nil {
		_ = ds.DepositLending(req.Address, req.Asset, req.Amount) // 回滾存款記錄
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "amount": req.Amount, "asset": req.Asset, "message": "取款成功"})
}

// handleDefiBorrow POST /api/defi/borrow {address,collateral_asset,collateral_amount,borrow_asset,borrow_amount}
func (s *RPCServer) handleDefiBorrow(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.defiSvc()
	if !ok || s.node == nil || s.node.walletSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "DeFi 未啟用")
		return
	}
	var req struct {
		Address          string  `json:"address"`
		CollateralAsset  string  `json:"collateral_asset"`
		CollateralAmount float64 `json:"collateral_amount"`
		BorrowAsset      string  `json:"borrow_asset"`
		BorrowAmount     float64 `json:"borrow_amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	req.Address = strings.TrimSpace(req.Address)
	req.CollateralAsset = normalizeAsset(req.CollateralAsset)
	req.BorrowAsset = normalizeAsset(req.BorrowAsset)
	if req.Address == "" || req.CollateralAmount <= 0 || req.BorrowAmount <= 0 {
		writeErr(w, http.StatusBadRequest, "缺少地址或金額無效")
		return
	}
	// 抵押品入池（費 2%）
	if _, err := s.node.walletSvc.Transfer(req.Address, defi.LendingAccount, wallet.Asset(req.CollateralAsset), amountOf(req.CollateralAmount, req.CollateralAsset), "defi:borrow:collateral"); err != nil {
		writeErr(w, http.StatusBadRequest, "抵押轉帳失敗："+err.Error())
		return
	}
	loanID, err := ds.Borrow(req.Address, req.CollateralAsset, req.CollateralAmount, req.BorrowAsset, req.BorrowAmount)
	if err != nil {
		_ = s.node.walletSvc.InternalTransfer(defi.LendingAccount, req.Address, wallet.Asset(req.CollateralAsset), amountOf(req.CollateralAmount, req.CollateralAsset), "defi:refund")
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// 借款由市場資金池發放（免二次費）
	if err := s.node.walletSvc.InternalTransfer(defi.LendingAccount, req.Address, wallet.Asset(req.BorrowAsset), amountOf(req.BorrowAmount, req.BorrowAsset), fmt.Sprintf("defi:borrow:%d", loanID)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "loan_id": loanID, "borrow_amount": req.BorrowAmount, "message": "借款成功"})
}

// handleDefiRepay POST /api/defi/repay {address,loan_id,amount}
func (s *RPCServer) handleDefiRepay(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.defiSvc()
	if !ok || s.node == nil || s.node.walletSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "DeFi 未啟用")
		return
	}
	var req struct {
		Address string  `json:"address"`
		LoanID  int64   `json:"loan_id"`
		Amount  float64 `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	req.Address = strings.TrimSpace(req.Address)
	if req.Address == "" || req.Amount <= 0 {
		writeErr(w, http.StatusBadRequest, "缺少地址或金額無效")
		return
	}
	loan, err := ds.LoanByID(req.LoanID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if loan == nil || loan.Status != "active" {
		writeErr(w, http.StatusBadRequest, "貸款不存在或已結清")
		return
	}
	if _, err := s.node.walletSvc.Transfer(req.Address, defi.LendingAccount, wallet.Asset(loan.BorrowAsset), amountOf(req.Amount, loan.BorrowAsset), "defi:repay"); err != nil {
		writeErr(w, http.StatusBadRequest, "還款轉帳失敗："+err.Error())
		return
	}
	result, err := ds.RepayLoan(req.Address, req.LoanID, req.Amount)
	if err != nil {
		_ = s.node.walletSvc.InternalTransfer(defi.LendingAccount, req.Address, wallet.Asset(loan.BorrowAsset), amountOf(req.Amount, loan.BorrowAsset), "defi:refund")
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// 全額還清 → 釋放抵押品
	if result["status"] == 1 {
		_ = s.node.walletSvc.InternalTransfer(defi.LendingAccount, req.Address, wallet.Asset(loan.CollateralAsset), amountOf(loan.CollateralAmount, loan.CollateralAsset), "defi:collateral-release")
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result, "message": "還款成功"})
}

// defiCheckBalance 檢查帳戶餘額（含 2% 費）。
func defiCheckBalance(acc *wallet.Account, asset string, amount float64) error {
	need := wallet.NewAmountF(amount*1.02, wallet.Asset(asset))
	bal := defiBalanceOf(acc, wallet.Asset(asset))
	if bal.Cmp(need) < 0 {
		return fmt.Errorf("%s 餘額不足（需含費）", asset)
	}
	return nil
}

// defiBalanceOf 取帳戶某資產餘額為 Amount。
func defiBalanceOf(acc *wallet.Account, asset wallet.Asset) wallet.Amount {
	switch asset {
	case wallet.AssetTACm:
		b := acc.TACmBalance
		if b == nil {
			b = new(big.Int)
		}
		return wallet.Amount{Big: b}
	case wallet.AssetTiUSD:
		return wallet.Amount{I64: acc.TiUSDBalance}
	default:
		return wallet.Amount{I64: acc.USDTBalance}
	}
}

// amountOf 構造 wallet 金額（6 位小數）。
func amountOf(amount float64, asset string) wallet.Amount {
	return wallet.NewAmountF(amount, wallet.Asset(asset))
}

// normalizeAsset 正規化資產名（TACm 首字母小寫 m 不可被 ToUpper 破壞）。
func normalizeAsset(a string) string {
	switch strings.ToUpper(strings.TrimSpace(a)) {
	case "TACM":
		return string(wallet.AssetTACm)
	case "TIUSD":
		return string(wallet.AssetTiUSD)
	case "USDT":
		return string(wallet.AssetUSDT)
	}
	return strings.ToUpper(strings.TrimSpace(a))
}

// poolTokens 依池 ID 回傳 token0/token1 資產名（seed 池固定）。
func poolTokens(poolID int64) (string, string) {
	switch poolID {
	case 1:
		return string(wallet.AssetTACm), string(wallet.AssetUSDT)
	case 2:
		return string(wallet.AssetTACm), string(wallet.AssetTiUSD)
	default:
		return string(wallet.AssetTiUSD), string(wallet.AssetUSDT)
	}
}

// ==================== IDO 平台 ====================

// handleDefiIDOProjects GET /api/defi/ido-projects
func (s *RPCServer) handleDefiIDOProjects(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.defiSvc()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "DeFi 未啟用")
		return
	}
	projects, err := ds.IDOProjects()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "projects": projects})
}

// handleDefiMyIDO GET /api/defi/my-ido?address=
func (s *RPCServer) handleDefiMyIDO(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.defiSvc()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "DeFi 未啟用")
		return
	}
	addr, err := defiAddr(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	subs, err := ds.MyIDO(addr)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "subscriptions": subs})
}

// handleDefiIDOSubscribe POST /api/defi/ido-subscribe {address,project_id,amount}
// 認購資金由用戶錢包轉入 IDO 資金池（鏈上費 2% 入資金池）。
func (s *RPCServer) handleDefiIDOSubscribe(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.defiSvc()
	if !ok || s.node == nil || s.node.walletSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "DeFi 未啟用")
		return
	}
	var req struct {
		Address   string  `json:"address"`
		ProjectID int64   `json:"project_id"`
		Amount    float64 `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	req.Address = strings.TrimSpace(req.Address)
	if req.Address == "" || req.Amount <= 0 {
		writeErr(w, http.StatusBadRequest, "缺少地址或金額無效")
		return
	}
	p, err := ds.GetIDOProject(req.ProjectID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if p == nil {
		writeErr(w, http.StatusNotFound, "項目不存在")
		return
	}
	if _, err := s.node.walletSvc.Transfer(req.Address, defi.IDOAccount, wallet.Asset(p.RaiseAsset), amountOf(req.Amount, p.RaiseAsset), fmt.Sprintf("defi:ido:%d", req.ProjectID)); err != nil {
		writeErr(w, http.StatusBadRequest, "認購轉帳失敗："+err.Error())
		return
	}
	tokens, err := ds.SubscribeIDO(req.Address, float64(req.ProjectID), req.Amount)
	if err != nil {
		_ = s.node.walletSvc.InternalTransfer(defi.IDOAccount, req.Address, wallet.Asset(p.RaiseAsset), amountOf(req.Amount, p.RaiseAsset), "defi:refund")
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tokens": tokens, "amount": req.Amount, "message": "認購成功"})
}

// handleDefiIDOClaim POST /api/defi/ido-claim/{id} {address}
// 項目 completed 後領取：分配代幣以 TACm 計價發放（token_price peg 1 TACm=1 USDT，鏈上 Reward 鑄造）。
func (s *RPCServer) handleDefiIDOClaim(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.defiSvc()
	if !ok || s.node == nil || s.node.walletSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "DeFi 未啟用")
		return
	}
	subID, err := postIDPath(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "subscription id 無效")
		return
	}
	addr, err := defiAddr(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	tokens, symbol, err := ds.ClaimIDOTokens(addr, subID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// 分配代幣等值：tokens × token_price（USDT）→ 以 TACm 發放（IDO peg 1:1）。
	sub, _ := ds.SubByID(subID)
	proj, _ := ds.GetIDOProject(sub.ProjectID)
	rewardTACM := tokens * proj.TokenPrice
	if err := s.node.walletSvc.Reward(addr, wallet.AssetTACm, amountOf(rewardTACM, string(wallet.AssetTACm)), fmt.Sprintf("defi:ido-claim:%d:%s", subID, symbol)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tokens": tokens, "symbol": symbol, "reward_tacm": rewardTACM, "message": "領取成功"})
}

// ==================== 收益聚合器（Vault） ====================

// handleDefiVaults GET /api/defi/vaults
func (s *RPCServer) handleDefiVaults(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.defiSvc()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "DeFi 未啟用")
		return
	}
	vaults, err := ds.Vaults()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "vaults": vaults})
}

// handleDefiMyVaults GET /api/defi/my-vaults?address=
func (s *RPCServer) handleDefiMyVaults(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.defiSvc()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "DeFi 未啟用")
		return
	}
	addr, err := defiAddr(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	positions, err := ds.MyVaults(addr)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "positions": positions})
}

// handleDefiVaultDeposit POST /api/defi/vault-deposit {address,vault_id,amount}
// 本金由用戶錢包轉入金庫帳戶（鏈上費 2% 入資金池）；金庫初始資產由鏈上基金注資。
func (s *RPCServer) handleDefiVaultDeposit(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.defiSvc()
	if !ok || s.node == nil || s.node.walletSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "DeFi 未啟用")
		return
	}
	var req struct {
		Address string  `json:"address"`
		VaultID int64   `json:"vault_id"`
		Amount  float64 `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	req.Address = strings.TrimSpace(req.Address)
	if req.Address == "" || req.Amount <= 0 {
		writeErr(w, http.StatusBadRequest, "缺少地址或金額無效")
		return
	}
	v, err := ds.GetVault(req.VaultID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if v == nil {
		writeErr(w, http.StatusNotFound, "金庫不存在")
		return
	}
	account := defi.VaultAccount(req.VaultID)
	if _, err := s.node.walletSvc.Transfer(req.Address, account, wallet.Asset(v.UnderlyingAsset), amountOf(req.Amount, v.UnderlyingAsset), fmt.Sprintf("defi:vault-dep:%d", req.VaultID)); err != nil {
		writeErr(w, http.StatusBadRequest, "存入轉帳失敗："+err.Error())
		return
	}
	shares, err := ds.DepositVault(req.Address, float64(req.VaultID), req.Amount)
	if err != nil {
		_ = s.node.walletSvc.InternalTransfer(account, req.Address, wallet.Asset(v.UnderlyingAsset), amountOf(req.Amount, v.UnderlyingAsset), "defi:refund")
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "shares": shares, "amount": req.Amount, "message": "存入成功"})
}

// handleDefiVaultWithdraw POST /api/defi/vault-withdraw {address,position_id,shares}
func (s *RPCServer) handleDefiVaultWithdraw(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.defiSvc()
	if !ok || s.node == nil || s.node.walletSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "DeFi 未啟用")
		return
	}
	var req struct {
		Address    string  `json:"address"`
		PositionID int64   `json:"position_id"`
		Shares     float64 `json:"shares"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	req.Address = strings.TrimSpace(req.Address)
	if req.Address == "" || req.Shares <= 0 {
		writeErr(w, http.StatusBadRequest, "缺少地址或份額無效")
		return
	}
	// 先取頭寸（全額取出後記錄會被刪除，需在刪除前拿 vault_id）。
	pos, _ := ds.VaultPosByID(req.PositionID)
	if pos == nil {
		writeErr(w, http.StatusNotFound, "頭寸不存在")
		return
	}
	amount, err := ds.WithdrawVault(req.Address, float64(req.PositionID), req.Shares)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// 金庫帳戶有 seed 注資＋用戶存入；取出走內部移轉（免二次費）。
	v, _ := ds.GetVault(pos.VaultID)
	if err := s.node.walletSvc.InternalTransfer(defi.VaultAccount(pos.VaultID), req.Address, wallet.Asset(v.UnderlyingAsset), amountOf(amount, v.UnderlyingAsset), "defi:vault-out"); err != nil {
		_, _ = ds.DepositVault(req.Address, float64(pos.VaultID), amount) // 回滾頭寸
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "amount": amount, "shares": req.Shares, "message": "取出成功"})
}

// handleDefiVaultCompound POST /api/defi/vault-compound/{id} {address}
// 復投收益由鏈上基金按策略 APR 派發（Reward 鑄造入金庫帳戶，用戶份額同額增加）。
func (s *RPCServer) handleDefiVaultCompound(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.defiSvc()
	if !ok || s.node == nil || s.node.walletSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "DeFi 未啟用")
		return
	}
	posID, err := postIDPath(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "position id 無效")
		return
	}
	addr, err := defiAddr(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	yield, newShares, err := ds.CompoundVault(addr, posID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	pos, _ := ds.VaultPosByID(posID)
	v, _ := ds.GetVault(pos.VaultID)
	if err := s.node.walletSvc.Reward(defi.VaultAccount(pos.VaultID), wallet.Asset(v.UnderlyingAsset), amountOf(yield, v.UnderlyingAsset), fmt.Sprintf("defi:vault-yield:%d", posID)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "yield": yield, "new_shares": newShares, "message": "復投成功"})
}
