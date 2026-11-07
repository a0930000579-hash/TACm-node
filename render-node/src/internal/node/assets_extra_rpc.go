package node

import (
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tacm/internal/wallet"
)

// handleTiUSDHistory GET /api/tiusd/history — TiUSD 累計鑄造趨勢（ledger mint 時間採樣）。
// 返回 { points: [{ts, minted_str, supply_str}], current: {supply, cap, burned, minted} }。
// mint 累加自 wallet ledger（KindMint/TiUSD）；末點以 tiusd_supply 當前值校正。
func (s *RPCServer) handleTiUSDHistory(w http.ResponseWriter, r *http.Request) {
	if s.node.walletSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "wallet_disabled"})
		return
	}
	points := 60
	if q := r.URL.Query().Get("points"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 && n <= 200 {
			points = n
		}
	}

	entries, err := s.node.walletSvc.LedgerByKindAsset(string(wallet.KindMint), string(wallet.AssetTiUSD))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	sup, err := s.node.walletSvc.TiUSDSummary()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	floor, capAmt, capEnabled, capErr := s.node.walletSvc.TiUSDCap()
	if capErr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": capErr.Error()})
		return
	}

	// 累加 minted（delta 為 micro 整數字串）。
	cum := int64(0)
	bucket := make([]map[string]any, 0, len(entries))
	step := 1
	if len(entries) > points {
		step = (len(entries) + points - 1) / points
	}
	for i, e := range entries {
		d, _ := strconv.ParseInt(e.Delta, 10, 64)
		cum += d
		if (i+1)%step == 0 || i == len(entries)-1 {
			bucket = append(bucket, map[string]any{
				"ts":     e.Ts,
				"minted": wallet.FmtThouI64(cum, 6, 6),
			})
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"points": bucket,
		"current": map[string]any{
			"supply":    wallet.FmtThouI64(sup.Supply, 6, 6),
			"cap":       wallet.FmtThouI64(capAmt, 6, 6),
			"cap_enabled": capEnabled,
			"floor":     wallet.FmtThouI64(floor, 6, 6),
			"minted":    wallet.FmtThouI64(sup.TotalMinted, 6, 6),
			"burned":    wallet.FmtThouI64(sup.TotalBurned, 6, 6),
			"health":    "ok",
		},
	})
}

// handleTiUSDPositions GET /api/tiusd/positions — 全鏈借款頭寸（defi loans 表）。
func (s *RPCServer) handleTiUSDPositions(w http.ResponseWriter, r *http.Request) {
	if s.node.defiSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "defi_disabled"})
		return
	}
	loans, err := s.node.defiSvc.AllLoans()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	totalBorrow, totalCollateral, active, err := s.node.defiSvc.LoanTotals()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	out := make([]map[string]any, 0, len(loans))
	for _, l := range loans {
		status := l.Status
		if status == "" {
			status = "active"
		}
		out = append(out, map[string]any{
			"id":                 l.ID,
			"address":            l.Address,
			"collateral_asset":   l.CollateralAsset,
			"collateral_amount":  strconv.FormatFloat(l.CollateralAmount, 'f', 6, 64),
			"borrow_asset":       l.BorrowAsset,
			"borrow_amount":      strconv.FormatFloat(l.BorrowAmount, 'f', 6, 64),
			"collateral_value":   strconv.FormatFloat(l.CollateralValue, 'f', 6, 64),
			"loan_value":         strconv.FormatFloat(l.LoanValue, 'f', 6, 64),
			"ltv":                l.LTV,
			"liquidation_price":  strconv.FormatFloat(l.LiquidationPrice, 'f', 6, 64),
			"interest_accrued":   strconv.FormatFloat(l.InterestAccrued, 'f', 6, 64),
			"status":             status,
			"created_at":         l.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"totals": map[string]any{
			"total_borrow":      strconv.FormatFloat(totalBorrow, 'f', 6, 64),
			"total_collateral":  strconv.FormatFloat(totalCollateral, 'f', 6, 64),
			"active_loans":      active,
		},
		"positions": out,
	})
}

// handleTiUSDExport GET /api/tiusd/export.csv — TiUSD 全量帳本 CSV。
// handleTACMExport   GET /api/tacm/export.csv — TACm 全量帳本 CSV。
func (s *RPCServer) handleAssetExport(w http.ResponseWriter, r *http.Request) {
	if s.node.walletSvc == nil {
		http.Error(w, "wallet_disabled", http.StatusServiceUnavailable)
		return
	}
	asset := strings.TrimPrefix(r.URL.Path, "/api/")
	if strings.HasPrefix(asset, "tiusd") {
		asset = string(wallet.AssetTiUSD)
	} else if strings.HasPrefix(asset, "tacm") {
		asset = string(wallet.AssetTACm)
	} else {
		http.Error(w, "unknown asset", http.StatusBadRequest)
		return
	}
	csv, err := s.node.walletSvc.ExportLedgerCSV(asset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+asset+`-ledger.csv"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(csv))
}

var _ = time.Now

// handleTiUSDReserveHistory GET /api/tiusd/reserve-history — 發行方儲備池 vs 流通供應歷史趨勢。
// 儲備＝TiUSD 帳本中 stable-pool＋defi_cdp＋fee 三個資金池帳戶的累計餘額（micro）；
// supply＝minted−burned 累加。返回 { points:[{ts, reserve_str, supply_str}], current:{reserve,supply,ratio} }。
func (s *RPCServer) handleTiUSDReserveHistory(w http.ResponseWriter, r *http.Request) {
	if s.node.walletSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "wallet_disabled"})
		return
	}
	points := 60
	if q := r.URL.Query().Get("points"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 && n <= 200 {
			points = n
		}
	}
	ledger, err := s.node.walletSvc.Ledger(5000)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	sup, err := s.node.walletSvc.TiUSDSummary()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	poolAccts := map[string]bool{"stable-pool": true, "defi_cdp": true, "fee": true}
	bal := map[string]int64{} // 帳戶累計餘額
	supply := int64(0)
	var bucket []map[string]any
	step := 1
	if len(ledger) > points {
		step = (len(ledger) + points - 1) / points
	}
	idx := 0
	for _, e := range ledger {
		canon, ok := wallet.NormalizeAsset(string(e.Asset))
		if !ok || canon != wallet.AssetTiUSD {
			continue
		}
		d, derr := strconv.ParseInt(e.Delta, 10, 64)
		if derr != nil {
			continue
		}
		bal[e.Account] += d
		if e.Kind == wallet.KindMint {
			supply += d
		} else if e.Kind == wallet.KindBurn {
			supply -= d
		}
		idx++
		if idx%step == 0 || idx == len(ledger)-1 {
			res := int64(0)
			for a := range poolAccts {
				res += bal[a]
			}
			bucket = append(bucket, map[string]any{
				"ts":       e.Ts,
				"reserve":  wallet.FmtThouI64(res, 6, 6),
				"supply":   wallet.FmtThouI64(supply, 6, 6),
				"reserve_raw": res,
			})
		}
	}
	// 末點以 tiusd_supply 當前值校正。
	resCur := int64(0)
	for a := range poolAccts {
		resCur += bal[a]
	}
	if len(bucket) > 0 {
		bucket[len(bucket)-1]["supply"] = wallet.FmtThouI64(sup.Supply, 6, 6)
	}
	ratio := 0.0
	if sup.Supply > 0 {
		ratio = float64(resCur) / float64(sup.Supply)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"points":  bucket,
		"current": map[string]any{
			"reserve": wallet.FmtThouI64(resCur, 6, 6),
			"supply":  wallet.FmtThouI64(sup.Supply, 6, 6),
			"ratio":   ratio,
			"pools":   "stable-pool · defi_cdp · fee",
		},
	})
}

// handleTACmReserveHistory GET /api/tacm/reserve-history — 已出塊產出 vs 供應上限歷史趨勢（TACm）。
// 產出累加自 wallet ledger（KindMint / reward / coinbase memo）；末點以 ChainMinedTacm 校正。
// 返回 { points:[{ts, mined, cap, util}], current:{mined, cap, ratio, cap_enabled} }。
func (s *RPCServer) handleTACmReserveHistory(w http.ResponseWriter, r *http.Request) {
	if s.node.walletSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "wallet_disabled"})
		return
	}
	points := 60
	if q := r.URL.Query().Get("points"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 && n <= 200 {
			points = n
		}
	}
	ledger, err := s.node.walletSvc.Ledger(5000)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	mined, err := s.node.walletSvc.ChainMinedTacm()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	capF := s.node.walletSvc.TACMSupplyCap()
	capBI := new(big.Int)
	if capF > 0 {
		capBI = new(big.Int).Mul(big.NewInt(int64(capF)), big.NewInt(1e18))
	}
	minedSum := new(big.Int)
	var bucket []map[string]any
	step := 1
	if len(ledger) > points {
		step = (len(ledger) + points - 1) / points
	}
	idx := 0
	for _, e := range ledger {
		canon, ok := wallet.NormalizeAsset(string(e.Asset))
		if !ok || canon != wallet.AssetTACm {
			continue
		}
		if e.Kind == wallet.KindMint || string(e.Kind) == "reward" || strings.Contains(e.Memo, "coinbase") {
			if d, ok2 := new(big.Int).SetString(e.Delta, 10); ok2 && d.Sign() > 0 {
				minedSum.Add(minedSum, d)
			}
		}
		idx++
		if idx%step == 0 || idx == len(ledger)-1 {
			util := 0.0
			if capBI.Sign() > 0 {
				mf := new(big.Float).SetInt(minedSum)
				cf := new(big.Float).SetInt(capBI)
				util, _ = new(big.Float).Quo(mf, cf).Float64()
			}
			bucket = append(bucket, map[string]any{
				"ts":    e.Ts,
				"mined": wallet.FmtThouBig(minedSum, 18, 6),
				"cap":   wallet.FmtThouBig(capBI, 18, 6),
				"util":  util,
			})
		}
	}
	if len(bucket) > 0 {
		bucket[len(bucket)-1]["mined"] = wallet.FmtThouBig(mined, 18, 6)
	}
	ratio := 0.0
	if capBI.Sign() > 0 {
		mf := new(big.Float).SetInt(mined)
		cf := new(big.Float).SetInt(capBI)
		ratio, _ = new(big.Float).Quo(mf, cf).Float64()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"points": bucket,
		"current": map[string]any{
			"mined":       wallet.FmtThouBig(mined, 18, 6),
			"cap":         wallet.FmtThouBig(capBI, 18, 6),
			"ratio":       ratio,
			"cap_enabled": capF > 0,
		},
	})
}

