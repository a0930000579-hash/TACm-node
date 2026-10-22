package node

// perp_rpc.go — 永續合約（Perpetuals）RPC handler，對標 dYdX / GMX。
// 路由（由整合者在 rpc.go 註冊）：
//   POST /api/perp/deposit        {address, asset, amount}
//   POST /api/perp/withdraw      {address, asset, amount}
//   POST /api/perp/position/open {address, symbol, side, leverage, margin, margin_asset}
//   POST /api/perp/position/close{position_id}
//   POST /api/perp/margin/add    {position_id, amount}
//   POST /api/perp/margin/remove {position_id, amount}
//   POST /api/perp/liquidate     {position_id, liquidator?}
//   GET  /api/perp/markets
//   GET  /api/perp/positions?trader=0x…
//   GET  /api/perp/account?trader=0x…

import (
	"net/http"
	"strings"

	"tacm/internal/wallet"
)

// pushPerpPrice 若 oracle 可用，把 symbol 最新價格推入 perp store。
func (s *RPCServer) pushPerpPrice(symbol string) {
	if s.node.oracleSvc == nil {
		return
	}
	p, ok := s.node.oracleSvc.Get(symbol)
	if ok && p.Price > 0 {
		s.node.perpSt.SetExternalPrice(symbol, p.Price)
	}
}

// perpAsset 正規化 perp 保證金資產字串 → wallet.Asset。
func perpAsset(asset string) (wallet.Asset, error) {
	switch strings.ToUpper(strings.TrimSpace(asset)) {
	case "TACM", "TACm":
		return wallet.AssetTACm, nil
	case "TIUSD":
		return wallet.AssetTiUSD, nil
	}
	return "", errPerpAsset
}

var errPerpAsset = &perpErr{"asset must be TACm or TIUSD"}

type perpErr struct{ s string }

func (e *perpErr) Error() string { return e.s }

// handlePerpDeposit POST /api/perp/deposit
func (s *RPCServer) handlePerpDeposit(w http.ResponseWriter, r *http.Request) {
	if s.node.perpSt == nil || s.node.walletSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "Perp 未啟用")
		return
	}
	var req struct {
		Address string  `json:"address"`
		Asset   string  `json:"asset"`
		Amount  float64 `json:"amount"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	addr, err := s.node.normalizeEthAddr(req.Address)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "address 無效")
		return
	}
	asset, err := perpAsset(req.Asset)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Amount <= 0 {
		writeErr(w, http.StatusBadRequest, "amount 必須 > 0")
		return
	}
	// 錢包轉帳：addr → perp_vault。
	if _, err := s.node.walletSvc.Transfer(addr, "perp_vault", asset,
		wallet.NewAmountF(req.Amount, asset), "perp deposit"); err != nil {
		writeErr(w, http.StatusBadRequest, "錢包轉帳失敗: "+err.Error())
		return
	}
	// perp 帳本記帳。
	if err := s.node.perpSt.Deposit(addr, string(asset), req.Amount); err != nil {
		// 退款。
		_ = s.node.walletSvc.InternalTransfer("perp_vault", addr, asset, wallet.NewAmountF(req.Amount, asset), "perp deposit refund")
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "address": addr, "asset": asset, "amount": req.Amount})
}

// handlePerpWithdraw POST /api/perp/withdraw
func (s *RPCServer) handlePerpWithdraw(w http.ResponseWriter, r *http.Request) {
	if s.node.perpSt == nil || s.node.walletSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "Perp 未啟用")
		return
	}
	var req struct {
		Address string  `json:"address"`
		Asset   string  `json:"asset"`
		Amount  float64 `json:"amount"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	addr, err := s.node.normalizeEthAddr(req.Address)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "address 無效")
		return
	}
	asset, err := perpAsset(req.Asset)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Amount <= 0 {
		writeErr(w, http.StatusBadRequest, "amount 必須 > 0")
		return
	}
	if err := s.node.perpSt.Withdraw(addr, string(asset), req.Amount); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// 從 perp_vault 退回錢包。
	if err := s.node.walletSvc.InternalTransfer("perp_vault", addr, asset,
		wallet.NewAmountF(req.Amount, asset), "perp withdraw"); err != nil {
		// 回滾 perp 帳本。
		_ = s.node.perpSt.Deposit(addr, string(asset), req.Amount)
		writeErr(w, http.StatusInternalServerError, "出金轉帳失敗: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "address": addr, "asset": asset, "amount": req.Amount})
}

// handlePerpOpen POST /api/perp/position/open
func (s *RPCServer) handlePerpOpen(w http.ResponseWriter, r *http.Request) {
	if s.node.perpSt == nil || s.node.walletSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "Perp 未啟用")
		return
	}
	var req struct {
		Address     string  `json:"address"`
		Symbol      string  `json:"symbol"`
		Side        string  `json:"side"`
		Leverage    float64 `json:"leverage"`
		Margin      float64 `json:"margin"`
		MarginAsset string  `json:"margin_asset"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	addr, err := s.node.normalizeEthAddr(req.Address)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "address 無效")
		return
	}
	s.pushPerpPrice(req.Symbol)
	p, notional, err := s.node.perpSt.OpenPosition(addr, req.Symbol, req.Side, req.Leverage, req.Margin, req.MarginAsset)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "position": p, "notional": notional})
}

// handlePerpClose POST /api/perp/position/close
func (s *RPCServer) handlePerpClose(w http.ResponseWriter, r *http.Request) {
	if s.node.perpSt == nil || s.node.walletSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "Perp 未啟用")
		return
	}
	var req struct {
		PositionID int64 `json:"position_id"`
	}
	if err := decodeJSON(r, &req); err != nil || req.PositionID <= 0 {
		writeErr(w, http.StatusBadRequest, "bad position_id")
		return
	}
	p, err := s.node.perpSt.GetPosition(req.PositionID)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	s.pushPerpPrice(p.Symbol)
	realized, returned, err := s.node.perpSt.ClosePosition(req.PositionID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "realized_pnl": realized, "returned": returned,
		"trader": p.Trader, "symbol": p.Symbol, "side": p.Side, "margin_asset": p.MarginAsset,
	})
}

// handlePerpMarginAdd POST /api/perp/margin/add
func (s *RPCServer) handlePerpMarginAdd(w http.ResponseWriter, r *http.Request) {
	if s.node.perpSt == nil {
		writeErr(w, http.StatusServiceUnavailable, "Perp 未啟用")
		return
	}
	var req struct {
		PositionID int64   `json:"position_id"`
		Amount     float64 `json:"amount"`
	}
	if err := decodeJSON(r, &req); err != nil || req.PositionID <= 0 {
		writeErr(w, http.StatusBadRequest, "bad position_id")
		return
	}
	if err := s.node.perpSt.AddMargin(req.PositionID, req.Amount); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handlePerpMarginRemove POST /api/perp/margin/remove
func (s *RPCServer) handlePerpMarginRemove(w http.ResponseWriter, r *http.Request) {
	if s.node.perpSt == nil {
		writeErr(w, http.StatusServiceUnavailable, "Perp 未啟用")
		return
	}
	var req struct {
		PositionID int64   `json:"position_id"`
		Amount     float64 `json:"amount"`
	}
	if err := decodeJSON(r, &req); err != nil || req.PositionID <= 0 {
		writeErr(w, http.StatusBadRequest, "bad position_id")
		return
	}
	if err := s.node.perpSt.RemoveMargin(req.PositionID, req.Amount); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handlePerpLiquidate POST /api/perp/liquidate
func (s *RPCServer) handlePerpLiquidate(w http.ResponseWriter, r *http.Request) {
	if s.node.perpSt == nil {
		writeErr(w, http.StatusServiceUnavailable, "Perp 未啟用")
		return
	}
	var req struct {
		PositionID int64  `json:"position_id"`
		Liquidator string `json:"liquidator"`
	}
	if err := decodeJSON(r, &req); err != nil || req.PositionID <= 0 {
		writeErr(w, http.StatusBadRequest, "bad position_id")
		return
	}
	p, err := s.node.perpSt.GetPosition(req.PositionID)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	s.pushPerpPrice(p.Symbol)
	liqBonus, returned, err := s.node.perpSt.LiquidatePosition(req.PositionID, req.Liquidator)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "liquidator": req.Liquidator, "liquidation_bonus": liqBonus,
		"returned_to_trader": returned, "trader": p.Trader, "symbol": p.Symbol,
	})
}

// handlePerpMarkets GET /api/perp/markets
func (s *RPCServer) handlePerpMarkets(w http.ResponseWriter, r *http.Request) {
	if s.node.perpSt == nil {
		writeErr(w, http.StatusServiceUnavailable, "Perp 未啟用")
		return
	}
	// 預熱所有市場價格。
	for _, sym := range []string{"BTC", "ETH", "SOL"} {
		s.pushPerpPrice(sym)
	}
	ms, err := s.node.perpSt.Markets()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "markets": ms})
}

// handlePerpPositions GET /api/perp/positions?trader=0x…
func (s *RPCServer) handlePerpPositions(w http.ResponseWriter, r *http.Request) {
	if s.node.perpSt == nil {
		writeErr(w, http.StatusServiceUnavailable, "Perp 未啟用")
		return
	}
	trader := strings.TrimSpace(r.URL.Query().Get("trader"))
	if trader == "" {
		writeErr(w, http.StatusBadRequest, "trader 為空")
		return
	}
	if _, err := s.node.normalizeEthAddr(trader); err != nil {
		writeErr(w, http.StatusBadRequest, "trader 地址無效")
		return
	}
	ps, err := s.node.perpSt.ListPositions(trader, true)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "positions": ps})
}

// handlePerpAccount GET /api/perp/account?trader=0x…
func (s *RPCServer) handlePerpAccount(w http.ResponseWriter, r *http.Request) {
	if s.node.perpSt == nil {
		writeErr(w, http.StatusServiceUnavailable, "Perp 未啟用")
		return
	}
	trader := strings.TrimSpace(r.URL.Query().Get("trader"))
	if trader == "" {
		writeErr(w, http.StatusBadRequest, "trader 為空")
		return
	}
	if _, err := s.node.normalizeEthAddr(trader); err != nil {
		writeErr(w, http.StatusBadRequest, "trader 地址無效")
		return
	}
	a, err := s.node.perpSt.GetAccount(trader)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "account": a})
}
