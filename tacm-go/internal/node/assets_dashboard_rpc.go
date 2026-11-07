package node

// v101：TACm 資產監控台後端——總覽（已出塊產出/總供應上限/剩餘可挖/利用率/健康）、
// 儲備池（獎勵池/手續費池/CDP 抵押鎖倉）、流向統計（coinbase 節點/池/礦工、兌換、
// 質押、跨鏈、一般轉帳）、跨鏈轉出/轉入明細、近期 TACm 流水。
// 與 v100 TiUSD 監控台整合為「資產中心」雙分頁（/assets：TACm＋TiUSD）。

import (
	"time"
	"math/big"
	"net/http"
	"strconv"
	"strings"

	"tacm/internal/defi"
	"tacm/internal/exchange"
	"tacm/internal/wallet"
)

// handleTACmDashboard GET /api/tacm/dashboard — TACm 監控台聚合資料。
func (s *RPCServer) handleTACMFlows(w http.ResponseWriter, r *http.Request) {
	// v130：5s 快取（沿用 dashboard 快取模式）——先前每輪詢全表掃 3000 筆，
	// 多頁輪詢同時打就是伺服器斷線元兇之一。
	if cached := s.dashGet("tacm-flows", 5*time.Second); cached != nil {
		writeJSON(w, http.StatusOK, cached)
		return
	}
	if s.node.walletSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "wallet_disabled"})
		return
	}
	ledger, err := s.node.walletSvc.Ledger(3000)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "流水查詢失敗: " + err.Error()})
		return
	}
	flow := []map[string]any{
		{"category": "coinbase_node", "in": new(big.Int), "out": new(big.Int)},
		{"category": "coinbase_pool", "in": new(big.Int), "out": new(big.Int)},
		{"category": "coinbase_miner", "in": new(big.Int), "out": new(big.Int)},
		{"category": "block_reward", "in": new(big.Int), "out": new(big.Int)},
		{"category": "swap", "in": new(big.Int), "out": new(big.Int)},
		{"category": "borrow", "in": new(big.Int), "out": new(big.Int)},
		{"category": "staking", "in": new(big.Int), "out": new(big.Int)},
		{"category": "cdp_lock", "in": new(big.Int), "out": new(big.Int)},
		{"category": "lp", "in": new(big.Int), "out": new(big.Int)},
		{"category": "yield_farm", "in": new(big.Int), "out": new(big.Int)},
		{"category": "ido", "in": new(big.Int), "out": new(big.Int)},
		{"category": "bridge", "in": new(big.Int), "out": new(big.Int)},
		{"category": "transfer", "in": new(big.Int), "out": new(big.Int)},
		{"category": "other", "in": new(big.Int), "out": new(big.Int)},
	}
	flowIdx := map[string]int{}
	for i, f := range flow {
		flowIdx[f["category"].(string)] = i
	}
	totalIn, totalOut := new(big.Int), new(big.Int)
	for _, e := range ledger {
		canon, ok := wallet.NormalizeAsset(string(e.Asset))
		if !ok || canon != wallet.AssetTACm {
			continue
		}
		delta, derr := strconv.ParseInt(e.Delta, 10, 64)
		if derr != nil {
			continue
		}
		cat := "transfer"
		m := e.Memo
		switch {
		case strings.Contains(m, "coinbase:node"):
			cat = "coinbase_node"
		case strings.Contains(m, "coinbase:pool"):
			cat = "coinbase_pool"
		case strings.Contains(m, "coinbase:miner"):
			cat = "coinbase_miner"
		case strings.Contains(m, "coinbase"):
			cat = "coinbase_miner"
		case strings.Contains(m, "block:") || string(e.Kind) == "reward":
			cat = "block_reward"
		case strings.Contains(m, "swap"):
			cat = "swap"
		case strings.Contains(m, "stake") || strings.Contains(m, "delegate") || strings.Contains(m, "liquid") || strings.Contains(m, "ls:"):
			cat = "staking"
		case strings.Contains(m, "defi:borrow") || strings.Contains(m, "cdp:borrow") || strings.Contains(m, "defi:repay"):
			cat = "borrow"
		case strings.Contains(m, "cdp:lock") || strings.Contains(m, "defi:cdp"):
			cat = "cdp_lock"
		case strings.Contains(m, "ido") || strings.Contains(m, "launch") || strings.Contains(m, "defi:ido"):
			cat = "ido"
		case strings.Contains(m, "farm") || strings.Contains(m, "yield") || strings.Contains(m, "defi:pool"):
			cat = "yield_farm"
		case strings.Contains(m, "pool") && !strings.Contains(m, "coinbase"):
			cat = "lp"
		case strings.Contains(m, "bridge"):
			cat = "bridge"
		case strings.Contains(m, "defi:"):
			cat = "other"
		}
		f := flow[flowIdx[cat]]
		d := big.NewInt(delta)
		if delta >= 0 {
			f["in"].(*big.Int).Add(f["in"].(*big.Int), d)
			totalIn.Add(totalIn, d)
		} else {
			d.Neg(d)
			f["out"].(*big.Int).Add(f["out"].(*big.Int), d)
			totalOut.Add(totalOut, d)
		}
	}
	out := map[string]any{
		"ok": true, "flows": flow, "total_in": totalIn.String(), "total_out": totalOut.String(),
		"updated": time.Now().Unix(),
	}
	s.dashSet("tacm-flows", out)
	writeJSON(w, http.StatusOK, out)
}

func (s *RPCServer) handleTACmDashboard(w http.ResponseWriter, r *http.Request) {
	if cached := s.dashGet("tacm", 5*time.Second); cached != nil {
		writeJSON(w, http.StatusOK, cached)
		return
	}
	if s.node.walletSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "wallet_disabled"})
		return
	}
	mined, err := s.node.walletSvc.ChainMinedTacm()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	capF := s.node.walletSvc.TACMSupplyCap()

	// 儲備池：獎勵池、手續費池、CDP 抵押鎖倉（TACm 側）。
	reserves := map[string]map[string]string{}
	for _, acct := range []string{wallet.RewardPoolAddr, exchange.FeeUID, defi.CDPAccount} {
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

	// 流向統計：ledger 中 TACm 相關分錄，依 memo 前綴分類。
	ledger, err := s.node.walletSvc.Ledger(3000)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "流水查詢失敗: " + err.Error()})
		return
	}
	flow := []map[string]any{
		{"category": "coinbase_node", "in": new(big.Int), "out": new(big.Int)},
		{"category": "coinbase_pool", "in": new(big.Int), "out": new(big.Int)},
		{"category": "coinbase_miner", "in": new(big.Int), "out": new(big.Int)},
		{"category": "block_reward", "in": new(big.Int), "out": new(big.Int)},
		{"category": "swap", "in": new(big.Int), "out": new(big.Int)},
		{"category": "borrow", "in": new(big.Int), "out": new(big.Int)},
		{"category": "staking", "in": new(big.Int), "out": new(big.Int)},
		{"category": "cdp_lock", "in": new(big.Int), "out": new(big.Int)},
		{"category": "lp", "in": new(big.Int), "out": new(big.Int)},
		{"category": "yield_farm", "in": new(big.Int), "out": new(big.Int)},
		{"category": "ido", "in": new(big.Int), "out": new(big.Int)},
		{"category": "bridge", "in": new(big.Int), "out": new(big.Int)},
		{"category": "transfer", "in": new(big.Int), "out": new(big.Int)},
		{"category": "other", "in": new(big.Int), "out": new(big.Int)},
	}
	flowIdx := map[string]int{}
	for i, f := range flow {
		flowIdx[f["category"].(string)] = i
	}
	var recent []map[string]any
	for _, e := range ledger {
		canon, ok := wallet.NormalizeAsset(string(e.Asset))
		if !ok || canon != wallet.AssetTACm {
			continue
		}
		delta, derr := strconv.ParseInt(e.Delta, 10, 64)
		if derr != nil {
			continue
		}
		cat := "transfer"
		m := e.Memo
		switch {
		case strings.Contains(m, "coinbase:node"):
			cat = "coinbase_node"
		case strings.Contains(m, "coinbase:pool"):
			cat = "coinbase_pool"
		case strings.Contains(m, "coinbase:miner"):
			cat = "coinbase_miner"
		case strings.Contains(m, "coinbase"):
			cat = "coinbase_miner"
		case strings.Contains(m, "block:") || string(e.Kind) == "reward":
			cat = "block_reward"
		case strings.Contains(m, "swap"):
			cat = "swap"
		case strings.Contains(m, "stake") || strings.Contains(m, "delegate") || strings.Contains(m, "liquid") || strings.Contains(m, "ls:"):
			cat = "staking"
		case strings.Contains(m, "defi:borrow") || strings.Contains(m, "cdp:borrow") || strings.Contains(m, "defi:repay"):
			cat = "borrow"
		case strings.Contains(m, "cdp:lock") || strings.Contains(m, "defi:cdp"):
			cat = "cdp_lock"
		case strings.Contains(m, "ido") || strings.Contains(m, "launch") || strings.Contains(m, "defi:ido"):
			cat = "ido"
		case strings.Contains(m, "farm") || strings.Contains(m, "yield") || strings.Contains(m, "defi:pool"):
			cat = "yield_farm"
		case strings.Contains(m, "pool") && !strings.Contains(m, "coinbase"):
			cat = "lp"
		case strings.Contains(m, "bridge"):
			cat = "bridge"
		case strings.Contains(m, "defi:cdp") || strings.Contains(m, "defi:borrow"):
			cat = "cdp_lock"
		case strings.Contains(m, "defi:stake") || strings.Contains(m, "defi:pool") || strings.Contains(m, "farm"):
			cat = "staking"
		case strings.Contains(m, "defi:"):
			cat = "other"
		}
		// v102：defi 質押/借款等關鍵字但未命中細類者歸類，其餘未匹配歸 other（不丟失流向）。
		if cat == "transfer" {
			matched := false
			for _, kw := range []string{"defi", "stake", "delegate", "cdp", "swap", "pool", "bridge", "reward", "airdrop", "grant", "faucet"} {
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
		d := new(big.Int).SetInt64(delta)
		if delta >= 0 {
			f["in"].(*big.Int).Add(f["in"].(*big.Int), d)
		} else {
			f["out"].(*big.Int).Add(f["out"].(*big.Int), new(big.Int).Neg(d))
		}
		if len(recent) < 20 {
			recent = append(recent, map[string]any{
				"ts": e.Ts, "account": e.Account, "delta": wallet.FormatAmountBig(new(big.Int).SetInt64(delta)),
				"memo": m, "kind": string(e.Kind),
			})
		}
	}

	// 跨鏈記錄（token=TACm；bridge 未啟用時空陣列）。
	bridgeList := []map[string]any{}
	if s.node.bridge != nil {
		if txs, berr := s.node.bridge.ListBridgeTxs(50, "TACM"); berr == nil {
			for _, tx := range txs {
				dir := "out"
				if !isTACChain(tx.SourceChain) && isTACChain(tx.TargetChain) {
					dir = "in"
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

	// 總覽：已出塊產出 vs 總供應上限。
	health := "halving_mode" // 減半模式（未設上限）
	utilization := 0.0
	remainingBI := new(big.Int) // v102-fix：剩餘可挖用 big.Int 精確計算（float→uint64 會溢出）
	if capF > 0 {
		capBig := new(big.Int).Mul(big.NewInt(int64(capF)), big.NewInt(1e18)) // 上限（最小單位）
		if capBig.Sign() > 0 {
			mf := new(big.Float).SetInt(mined)
			cf := new(big.Float).SetInt(capBig)
			utilization, _ = new(big.Float).Quo(mf, cf).Float64()
			switch {
			case mined.Cmp(capBig) > 0:
				health = "over_cap"
			case utilization >= 0.95:
				health = "near_cap"
			default:
				health = "ok"
			}
			remainingBI = new(big.Int).Sub(capBig, mined)
			if remainingBI.Sign() < 0 {
				remainingBI.SetInt64(0)
			}
		}
	}

	flowOut := make([]map[string]any, 0, len(flow))
	for _, f := range flow {
		inBI := f["in"].(*big.Int)
		outBI := f["out"].(*big.Int)
		netBI := new(big.Int).Sub(inBI, outBI)
		flowOut = append(flowOut, map[string]any{
			"category": f["category"],
			"in":       wallet.FormatAmountBig(inBI),
			"out":      wallet.FormatAmountBig(outBI),
			"net":      wallet.FormatAmountBig(netBI),
		})
	}
	// v102：TACm 市價（TiUSD 市值錨定基準）。
	tacmPrice := s.node.walletSvc.TACMPrice()
	listed := tacmPrice > 0

	out := map[string]any{
		"ok": true,
		"overview": map[string]any{
			"mined":        wallet.FmtThouBig(mined, 18, 6),
			"mined_raw":    mined.String(),
			"cap":          capF,
			"cap_enabled":  capF > 0,
			"remaining":    wallet.FmtThouBig(remainingBI, 18, 6),
			"utilization":  utilization,
			"util_warning": utilWarning(utilization),
			"health":       health,
			"listed":       listed,
			"tacm_price":   tacmPrice,
			"peg_note":     "TACm 為原生幣：出塊獎勵（coinbase）產生，45% 節點 / 15% 獎勵池 / 40% 礦工",
		},
		"reserves": reserves,
		"flow":     flowOut,
		"bridge":   bridgeList,
		"recent":   recent,
	}
	s.dashSet("tacm", out)
	writeJSON(w, http.StatusOK, out)
}
