package node

// v100：TiUSD 穩定幣監控台後端——總覽（供應/上限/下限/剩餘/利用率/健康）、
// 儲備（stable-pool/fee 池）、流向統計（ledger memo 分類）、跨鏈轉出/轉入明細、
// 近期 TiUSD 流水。確保總供應不超額膨脹（supply ≤ cap 檢查）。

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tacm/internal/wallet"
)

// handleTiUSDDashboard GET /api/tiusd/dashboard — TiUSD 監控台聚合資料。
func (s *RPCServer) handleTiUSDDashboard(w http.ResponseWriter, r *http.Request) {
	if cached := s.dashGet("tiusd", 5*time.Second); cached != nil {
		writeJSON(w, http.StatusOK, cached)
		return
	}
	if s.node.walletSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "wallet_disabled"})
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

	// 儲備：兌換池與手續費池三資產餘額。
	reserves := map[string]map[string]string{}
	for _, acct := range []string{"stable-pool", "fee"} {
		acc, err := s.node.walletSvc.Balance(acct)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "儲備查詢失敗: " + err.Error()})
			return
		}
		reserves[acct] = map[string]string{
			"usdt":  wallet.FormatAmountI64(acc.USDTBalance, wallet.AssetUSDT),
			"tiusd": wallet.FormatAmountI64(acc.TiUSDBalance, wallet.AssetTiUSD),
			"tacm":  wallet.FormatAmountBig(acc.TACmBalance),
		}
	}

	// 流向統計：ledger 中 TiUSD 相關分錄，依 memo 前綴分類流入/流出。
	ledger, err := s.node.walletSvc.Ledger(2000)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "流水查詢失敗: " + err.Error()})
		return
	}
	flow := []map[string]any{
		{"category": "cdp_mint", "in": int64(0), "out": int64(0), "net": int64(0)},
		{"category": "repay_burn", "in": int64(0), "out": int64(0), "net": int64(0)},
		{"category": "stable_swap", "in": int64(0), "out": int64(0), "net": int64(0)},
		{"category": "native_swap", "in": int64(0), "out": int64(0), "net": int64(0)},
		{"category": "pool_seed", "in": int64(0), "out": int64(0), "net": int64(0)},
		{"category": "floor_topup", "in": int64(0), "out": int64(0), "net": int64(0)},
		{"category": "bridge", "in": int64(0), "out": int64(0), "net": int64(0)},
		{"category": "transfer", "in": int64(0), "out": int64(0), "net": int64(0)},
		{"category": "other", "in": int64(0), "out": int64(0), "net": int64(0)},
	}
	flowIdx := map[string]int{}
	for i, f := range flow {
		flowIdx[f["category"].(string)] = i
	}
	var recent []map[string]any
	for _, e := range ledger {
		canon, ok := wallet.NormalizeAsset(string(e.Asset))
		if !ok || canon != wallet.AssetTiUSD {
			continue
		}
		delta, derr := strconv.ParseInt(e.Delta, 10, 64)
		if derr != nil {
			continue
		}
		cat := "transfer"
		m := e.Memo
		switch {
		case strings.Contains(m, "cdp:mint") || strings.Contains(m, "defi:borrow"):
			cat = "cdp_mint"
		case strings.Contains(m, "defi:repay") || strings.Contains(m, "cdp:repay") || strings.Contains(m, "repay") || strings.Contains(m, "burn"):
			cat = "repay_burn"
		case strings.Contains(m, "stable:swap"):
			cat = "stable_swap"
		case strings.Contains(m, "native:swap"):
			cat = "native_swap"
		case strings.Contains(m, "chain-mint") || strings.Contains(m, ":seed"):
			cat = "pool_seed"
		case strings.Contains(m, "supply:floor-topup"):
			cat = "floor_topup"
		case strings.Contains(m, "bridge"):
			cat = "bridge"
		case strings.Contains(m, "supply:cap-burn"):
			cat = "repay_burn"
		}
		// v102：未匹配的分類歸 other（不丟失任何 TiUSD 流向）。
		if cat == "transfer" && !strings.HasPrefix(m, "") {
			matched := false
			for _, kw := range []string{"cdp", "defi", "swap", "seed", "supply", "bridge", "burn", "repay", "native", "stable", "chain-mint"} {
				if strings.Contains(m, kw) {
					matched = true
					break
				}
			}
			if matched {
				cat = "other"
			}
		}
		f := flow[flowIdx[cat]]
		if delta >= 0 {
			f["in"] = f["in"].(int64) + delta
		} else {
			f["out"] = f["out"].(int64) - delta
		}
		f["net"] = f["in"].(int64) - f["out"].(int64)
		if len(recent) < 20 {
			recent = append(recent, map[string]any{
				"ts": e.Ts, "account": e.Account, "delta": wallet.FormatAmountI64(delta, wallet.AssetTiUSD),
				"memo": m, "kind": string(e.Kind),
			})
		}
	}

	// 跨鏈記錄（token=TiUSD 大小寫不敏感；bridge 未啟用時返回空陣列）。
	bridgeList := []map[string]any{}
	bridgeIn, bridgeOut, bridgeDone := 0.0, 0.0, int64(0)
	if s.node.bridge != nil {
		if txs, berr := s.node.bridge.ListBridgeTxs(50, "TIUSD"); berr == nil {
			for _, tx := range txs {
				dir := "out" // 從 TAC 鏈轉出
				if !isTACChain(tx.SourceChain) && isTACChain(tx.TargetChain) {
					dir = "in"
				}
				// v104：跨鏈淨額聚合（completed 才計入）。
				if tx.Status == "completed" || tx.Status == "confirmed" {
					if dir == "in" {
						bridgeIn += tx.Amount
					} else {
						bridgeOut += tx.Amount
					}
					bridgeDone++
				}
				bridgeList = append(bridgeList, map[string]any{
					"bridge_tx_id": tx.BridgeTxID, "direction": dir,
					"source_chain": tx.SourceChain, "target_chain": tx.TargetChain,
					"source_address": tx.SourceAddress, "target_address": tx.TargetAddress,
					"amount": tx.Amount, "fee": tx.Fee, "received_amount": tx.ReceivedAmount,
					"token": tx.Token, "status": tx.Status,
					"source_tx_hash": tx.SourceTxHash, "target_tx_hash": tx.TargetTxHash,
					"created_at": tx.CreatedAt,
				})
			}
		}
	}

	// 膨脹檢查：supply 是否超過 cap。
	// v102：TACm 市價與上市狀態（市值錨定顯示）。
	tacmPrice := s.node.walletSvc.TACMPrice()
	listed := tacmPrice > 0

	health := "cap_disabled"
	utilization := 0.0
	if capEnabled && capAmt > 0 {
		utilization = float64(sup.Supply) / float64(capAmt)
		switch {
		case !listed:
			// v102：未上市時 cap＝floor（0.330 基準），供給恆≈cap 屬設計使然，不報 near/over。
			health = "unlisted"
		case sup.Supply > capAmt:
			health = "over_cap"
		case utilization >= 0.95:
			health = "near_cap"
		default:
			health = "ok"
		}
	}

	remaining := int64(0)
	if capEnabled {
		remaining = capAmt - sup.Supply
		if remaining < 0 {
			remaining = 0
		}
	}
	out := map[string]any{
		"ok": true,
		"overview": map[string]any{
			"supply":         wallet.FmtThouI64(sup.Supply, 6, 6),
			"supply_raw":     sup.Supply,
			"cap":            wallet.FmtThouI64(capAmt, 6, 6),
			"cap_raw":        capAmt,
			"cap_enabled":    capEnabled,
			"cap_mode":       "mcap",
			"listed":         listed,
			"tacm_price":     tacmPrice,
			"floor":          wallet.FmtThouI64(floor, 6, 6),
			"floor_raw":      floor,
			"remaining":      wallet.FmtThouI64(remaining, 6, 6),
			"remaining_raw":  remaining,
			"utilization":    utilization,
			"util_warning":   utilWarning(utilization),
			"peg_price":      "1.0",
			"total_minted":   wallet.FmtThouI64(sup.TotalMinted, 6, 6),
			"total_burned":   wallet.FmtThouI64(sup.TotalBurned, 6, 6),
			"net_minted":     wallet.FmtThouI64(sup.TotalMinted-sup.TotalBurned, 6, 6),
			"health":         health,
			"updated_ts":     sup.UpdatedTs,
		},
		"reserves": reserves,
		"flow":     formatTiUSDFlow(flow),
		"bridge":   bridgeList,
		"bridge_summary": map[string]any{
			"in_total":  strconv.FormatFloat(bridgeIn, 'f', 6, 64),
			"out_total": strconv.FormatFloat(bridgeOut, 'f', 6, 64),
			"net":       strconv.FormatFloat(bridgeIn-bridgeOut, 'f', 6, 64),
			"done":      bridgeDone,
		},
		"recent":   recent,
	}
	s.dashSet("tiusd", out)
	writeJSON(w, http.StatusOK, out)
}

// isTACChain 判斷鏈名是否為 TAC 主鏈（大小寫不敏感）。
func isTACChain(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "tac") || strings.Contains(n, "titan")
}

var _ = json.Marshal // 保留 import（後續擴展）
var _ = time.Now

// formatTiUSDFlow v106：TiUSD flow 累加值為 1e6 最小單位，輸出前轉十進制字串
// （避免前端把 micro 原值當整數顯示千分位逗號而無小數點）。
func formatTiUSDFlow(flow []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(flow))
	for _, f := range flow {
		in := f["in"].(int64)
		ov := f["out"].(int64)
		out = append(out, map[string]any{
			"category": f["category"],
			"in":       wallet.FormatAmountI64(in, wallet.AssetTiUSD),
			"out":      wallet.FormatAmountI64(ov, wallet.AssetTiUSD),
			"net":      wallet.FormatAmountI64(in-ov, wallet.AssetTiUSD),
		})
	}
	return out
}
