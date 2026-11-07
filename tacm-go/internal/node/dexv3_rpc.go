package node

// dexv3_rpc.go — DEX V3 集中流動性 RPC（對標 Uniswap V3）：
//   POST /api/dexv3/pool/create      建池（token0/token1/費率分級/初始√P）
//   POST /api/dexv3/position/mint    mint 新位置（回傳 positionId、實際 amount0/amount1、liquidity）
//   POST /api/dexv3/position/increase 增加位置流動性
//   POST /api/dexv3/position/decrease 撤部分/全部流動性（退回 token）
//   POST /api/dexv3/position/collect  領取累積費用
//   POST /api/dexv3/swap            集中流動性 swap（跨 tick 吃單）
//   GET  /api/dexv3/pools            池列表
//   GET  /api/dexv3/positions?address= 我的位置
//   GET  /api/dexv3/pool?poolId=      單池詳情
//
// 資金進出：dexv3 包只維護狀態與數學；此 handler 用 wallet 服務把使用者與池帳戶
// "dexv3_pool_<poolId>" 對接。mint/increase 先收「期望投入上限」，依實際使用量退差額。

import (
	"net/http"
	"strconv"
	"strings"

	"tacm/internal/wallet"
)

// dexV3PoolAcct 池帳戶名。
func dexV3PoolAcct(poolId int64) string { return "dexv3_pool_" + strconv.FormatInt(poolId, 10) }

// dexV3Asset 池 token 名 → wallet.Asset。
func dexV3Asset(name string) (wallet.Asset, bool) {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "TACM":
		return wallet.AssetTACm, true
	case "TIUSD", "TUSD":
		return wallet.AssetTiUSD, true
	default:
		return "", false
	}
}

// dexV3Up 檢查服務可用性。
func (s *RPCServer) dexV3Up(w http.ResponseWriter) bool {
	if s.node.dexv3St == nil || s.node.walletSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "DEX V3 未啟用")
		return false
	}
	return true
}

// dexV3MoveOptional 金額 > 0 時才轉帳（Transfer 不接受 0）。from→to 收費用。
func (s *RPCServer) dexV3MoveOptional(w http.ResponseWriter, from, to string, asset wallet.Asset, amount float64, memo string) bool {
	if amount <= 0 {
		return true
	}
	if _, err := s.node.walletSvc.Transfer(from, to, asset, wallet.NewAmountF(amount, asset), memo); err != nil {
		writeErr(w, http.StatusBadRequest, "資金轉入池失敗："+err.Error())
		return false
	}
	return true
}

// ===== POST /api/dexv3/pool/create =====
func (s *RPCServer) handleDexV3CreatePool(w http.ResponseWriter, r *http.Request) {
	if !s.dexV3Up(w) {
		return
	}
	var req struct {
		Address   string  `json:"address"`
		Token0    string  `json:"token0"`
		Token1    string  `json:"token1"`
		FeeTier   int     `json:"fee_tier"`
		SqrtPrice float64 `json:"sqrt_price"` // 選填；預設 1.0（price=1）
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	addr := strings.TrimSpace(req.Address)
	if _, err := s.node.normalizeEthAddr(addr); err != nil && addr != "" {
		// 建池不需持幣，地址僅記錄為建立者；容許空字串，但格式若給錯則擋下。
		writeErr(w, http.StatusBadRequest, "address 格式無效")
		return
	}
	a0, ok0 := dexV3Asset(req.Token0)
	a1, ok1 := dexV3Asset(req.Token1)
	if !ok0 || !ok1 {
		writeErr(w, http.StatusBadRequest, "token 僅支援 TACm / TIUSD")
		return
	}
	if a0 == a1 {
		writeErr(w, http.StatusBadRequest, "token0 不可等於 token1")
		return
	}
	if req.SqrtPrice <= 0 {
		req.SqrtPrice = 1.0
	}
	p, err := s.node.dexv3St.CreatePool(string(a0), string(a1), req.FeeTier, req.SqrtPrice)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "pool": p,
		"pool_account": dexV3PoolAcct(p.Id),
		"message":      "池建立成功",
	})
}

// ===== POST /api/dexv3/position/mint =====
func (s *RPCServer) handleDexV3MintPosition(w http.ResponseWriter, r *http.Request) {
	if !s.dexV3Up(w) {
		return
	}
	var req struct {
		Address   string  `json:"address"`
		PoolID    int64   `json:"pool_id"`
		TickLower int     `json:"tick_lower"`
		TickUpper int     `json:"tick_upper"`
		Amount0   float64 `json:"amount0"`
		Amount1   float64 `json:"amount1"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	addr := strings.TrimSpace(req.Address)
	if _, err := s.node.normalizeEthAddr(addr); err != nil {
		writeErr(w, http.StatusBadRequest, "address 格式無效（需 0x 位址）")
		return
	}
	if req.PoolID <= 0 {
		writeErr(w, http.StatusBadRequest, "缺少 pool_id")
		return
	}
	pool, err := s.node.dexv3St.GetPool(req.PoolID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	asset0, _ := dexV3Asset(pool.Token0)
	asset1, _ := dexV3Asset(pool.Token1)
	poolAcct := dexV3PoolAcct(pool.Id)

	// 1) 先把期望投入上限收進池帳戶。
	if !s.dexV3MoveOptional(w, addr, poolAcct, asset0, req.Amount0, "dexv3 mint:token0") {
		return
	}
	if !s.dexV3MoveOptional(w, addr, poolAcct, asset1, req.Amount1, "dexv3 mint:token1") {
		return
	}
	// 2) 建立位置（回傳實際使用量）。
	res, err := s.node.dexv3St.MintPosition(addr, req.PoolID, req.TickLower, req.TickUpper, req.Amount0, req.Amount1)
	if err != nil {
		// 退回已收資金。
		_ = s.node.walletSvc.InternalTransfer(poolAcct, addr, asset0, wallet.NewAmountF(req.Amount0, asset0), "dexv3 mint refund:token0")
		_ = s.node.walletSvc.InternalTransfer(poolAcct, addr, asset1, wallet.NewAmountF(req.Amount1, asset1), "dexv3 mint refund:token1")
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// 3) 退差額（期望 − 實際）。
	refund0 := req.Amount0 - res.Amount0
	refund1 := req.Amount1 - res.Amount1
	if refund0 > 0 {
		_ = s.node.walletSvc.InternalTransfer(poolAcct, addr, asset0, wallet.NewAmountF(refund0, asset0), "dexv3 mint refund:diff0")
	}
	if refund1 > 0 {
		_ = s.node.walletSvc.InternalTransfer(poolAcct, addr, asset1, wallet.NewAmountF(refund1, asset1), "dexv3 mint refund:diff1")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "position_id": res.PositionId,
		"liquidity": res.Liquidity,
		"amount0":   res.Amount0, "amount1": res.Amount1,
		"refund0": refund0, "refund1": refund1,
		"message": "位置建立成功",
	})
}

// ===== POST /api/dexv3/position/increase =====
func (s *RPCServer) handleDexV3IncreasePosition(w http.ResponseWriter, r *http.Request) {
	if !s.dexV3Up(w) {
		return
	}
	var req struct {
		PositionID int64   `json:"position_id"`
		Address    string  `json:"address"`
		Amount0    float64 `json:"amount0"`
		Amount1    float64 `json:"amount1"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	addr := strings.TrimSpace(req.Address)
	pos, err := s.node.dexv3St.GetPosition(req.PositionID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !strings.EqualFold(pos.Owner, addr) {
		writeErr(w, http.StatusForbidden, "非位置持有人")
		return
	}
	pool, err := s.node.dexv3St.GetPool(pos.PoolId)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
	}
	asset0, _ := dexV3Asset(pool.Token0)
	asset1, _ := dexV3Asset(pool.Token1)
	poolAcct := dexV3PoolAcct(pool.Id)

	if !s.dexV3MoveOptional(w, addr, poolAcct, asset0, req.Amount0, "dexv3 increase:token0") {
		return
	}
	if !s.dexV3MoveOptional(w, addr, poolAcct, asset1, req.Amount1, "dexv3 increase:token1") {
		return
	}
	res, err := s.node.dexv3St.IncreasePosition(req.PositionID, req.Amount0, req.Amount1)
	if err != nil {
		_ = s.node.walletSvc.InternalTransfer(poolAcct, addr, asset0, wallet.NewAmountF(req.Amount0, asset0), "dexv3 increase refund:token0")
		_ = s.node.walletSvc.InternalTransfer(poolAcct, addr, asset1, wallet.NewAmountF(req.Amount1, asset1), "dexv3 increase refund:token1")
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	refund0 := req.Amount0 - res.Amount0
	refund1 := req.Amount1 - res.Amount1
	if refund0 > 0 {
		_ = s.node.walletSvc.InternalTransfer(poolAcct, addr, asset0, wallet.NewAmountF(refund0, asset0), "dexv3 increase refund:diff0")
	}
	if refund1 > 0 {
		_ = s.node.walletSvc.InternalTransfer(poolAcct, addr, asset1, wallet.NewAmountF(refund1, asset1), "dexv3 increase refund:diff1")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "position_id": res.PositionId,
		"liquidity": res.Liquidity,
		"amount0":   res.Amount0, "amount1": res.Amount1,
		"message": "流動性已增加",
	})
}

// ===== POST /api/dexv3/position/decrease =====
func (s *RPCServer) handleDexV3DecreasePosition(w http.ResponseWriter, r *http.Request) {
	if !s.dexV3Up(w) {
		return
	}
	var req struct {
		PositionID int64   `json:"position_id"`
		Address    string  `json:"address"`
		Liquidity  float64 `json:"liquidity"` // 要撤出的 L；<=0 視為全撤
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	addr := strings.TrimSpace(req.Address)
	pos, err := s.node.dexv3St.GetPosition(req.PositionID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
	}
	if !strings.EqualFold(pos.Owner, addr) {
		writeErr(w, http.StatusForbidden, "非位置持有人")
		return
	}
	pool, err := s.node.dexv3St.GetPool(pos.PoolId)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
	}
	asset0, _ := dexV3Asset(pool.Token0)
	asset1, _ := dexV3Asset(pool.Token1)
	poolAcct := dexV3PoolAcct(pool.Id)

	removeL := req.Liquidity
	if removeL <= 0 {
		removeL = pos.Liquidity
	}
	res, err := s.node.dexv3St.DecreasePosition(req.PositionID, removeL)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if res.Amount0 > 0 {
		_ = s.node.walletSvc.InternalTransfer(poolAcct, addr, asset0, wallet.NewAmountF(res.Amount0, asset0), "dexv3 decrease:token0")
	}
	if res.Amount1 > 0 {
		_ = s.node.walletSvc.InternalTransfer(poolAcct, addr, asset1, wallet.NewAmountF(res.Amount1, asset1), "dexv3 decrease:token1")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "position_id": res.PositionId,
		"liquidity_remaining": res.LiquidityRemaining,
		"amount0":             res.Amount0, "amount1": res.Amount1,
		"message": "流動性已撤出",
	})
}

// ===== POST /api/dexv3/position/collect =====
func (s *RPCServer) handleDexV3Collect(w http.ResponseWriter, r *http.Request) {
	if !s.dexV3Up(w) {
		return
	}
	var req struct {
		PositionID int64  `json:"position_id"`
		Address    string `json:"address"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	addr := strings.TrimSpace(req.Address)
	pos, err := s.node.dexv3St.GetPosition(req.PositionID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
	}
	if !strings.EqualFold(pos.Owner, addr) {
		writeErr(w, http.StatusForbidden, "非位置持有人")
		return
	}
	pool, err := s.node.dexv3St.GetPool(pos.PoolId)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
	}
	asset0, _ := dexV3Asset(pool.Token0)
	asset1, _ := dexV3Asset(pool.Token1)
	poolAcct := dexV3PoolAcct(pool.Id)

	res, err := s.node.dexv3St.CollectFees(req.PositionID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if res.Amount0 > 0 {
		_ = s.node.walletSvc.InternalTransfer(poolAcct, addr, asset0, wallet.NewAmountF(res.Amount0, asset0), "dexv3 collect:token0")
	}
	if res.Amount1 > 0 {
		_ = s.node.walletSvc.InternalTransfer(poolAcct, addr, asset1, wallet.NewAmountF(res.Amount1, asset1), "dexv3 collect:token1")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "position_id": res.PositionId,
		"amount0": res.Amount0, "amount1": res.Amount1,
		"message": "費用已領取",
	})
}

// ===== POST /api/dexv3/swap =====
func (s *RPCServer) handleDexV3Swap(w http.ResponseWriter, r *http.Request) {
	if !s.dexV3Up(w) {
		return
	}
	var req struct {
		Address    string  `json:"address"`
		PoolID     int64   `json:"pool_id"`
		ZeroForOne bool    `json:"zero_for_one"`
		AmountIn   float64 `json:"amount_in"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	addr := strings.TrimSpace(req.Address)
	if _, err := s.node.normalizeEthAddr(addr); err != nil {
		writeErr(w, http.StatusBadRequest, "address 格式無效（需 0x 位址）")
		return
	}
	if req.AmountIn <= 0 {
		writeErr(w, http.StatusBadRequest, "amount_in 必須為正")
		return
	}
	pool, err := s.node.dexv3St.GetPool(req.PoolID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	inAsset, outAsset := dexV3Assets(pool.Token0, pool.Token1, req.ZeroForOne)
	poolAcct := dexV3PoolAcct(pool.Id)

	// 輸入 token：使用者 → 池。
	if _, err := s.node.walletSvc.Transfer(addr, poolAcct, inAsset, wallet.NewAmountF(req.AmountIn, inAsset), "dexv3 swap:in"); err != nil {
		writeErr(w, http.StatusBadRequest, "輸入轉入池失敗："+err.Error())
		return
	}
	res, err := s.node.dexv3St.Swap(req.PoolID, req.ZeroForOne, req.AmountIn)
	if err != nil {
		// 退回輸入。
		_ = s.node.walletSvc.InternalTransfer(poolAcct, addr, inAsset, wallet.NewAmountF(req.AmountIn, inAsset), "dexv3 swap refund:in")
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// 輸出 token：池 → 使用者。
	if res.AmountOut > 0 {
		_ = s.node.walletSvc.InternalTransfer(poolAcct, addr, outAsset, wallet.NewAmountF(res.AmountOut, outAsset), "dexv3 swap:out")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"zero_for_one": res.ZeroForOne,
		"amount_in":    res.AmountIn,
		"amount_out":   res.AmountOut,
		"fee":          res.Fee,
		"sqrt_price":   res.SqrtPrice,
		"tick":         res.Tick,
		"liquidity":    res.Liquidity,
		"price":        res.SqrtPrice * res.SqrtPrice,
		"message":      "swap 完成",
	})
}

// dexV3Assets 依方向回傳輸入/輸出資產。
// zeroForOne=true → 輸入 token0、輸出 token1；反之輸入 token1、輸出 token0。
func dexV3Assets(token0, token1 string, zeroForOne bool) (inAsset, outAsset wallet.Asset) {
	a0, _ := dexV3Asset(token0)
	a1, _ := dexV3Asset(token1)
	if zeroForOne {
		return a0, a1
	}
	return a1, a0
}

// ===== GET 端點 =====

// handleDexV3Pools GET /api/dexv3/pools
func (s *RPCServer) handleDexV3Pools(w http.ResponseWriter, r *http.Request) {
	if !s.dexV3Up(w) {
		return
	}
	pools, err := s.node.dexv3St.ListPools()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pools": pools})
}

// handleDexV3Positions GET /api/dexv3/positions?address=
func (s *RPCServer) handleDexV3Positions(w http.ResponseWriter, r *http.Request) {
	if !s.dexV3Up(w) {
		return
	}
	addr := strings.TrimSpace(r.URL.Query().Get("address"))
	if addr == "" {
		writeErr(w, http.StatusBadRequest, "缺少 address")
		return
	}
	pos, err := s.node.dexv3St.ListPositions(addr)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "positions": pos})
}

// handleDexV3Pool GET /api/dexv3/pool?poolId=
func (s *RPCServer) handleDexV3Pool(w http.ResponseWriter, r *http.Request) {
	if !s.dexV3Up(w) {
		return
	}
	idStr := strings.TrimSpace(r.URL.Query().Get("poolId"))
	poolId, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || poolId <= 0 {
		writeErr(w, http.StatusBadRequest, "缺少/非法 poolId")
		return
	}
	p, err := s.node.dexv3St.GetPool(poolId)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pool": p})
}
