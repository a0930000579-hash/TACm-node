package node

import (
	"encoding/json"
	"net/http"
	"os"

	"tacm/internal/exchange"
	"tacm/internal/wallet"
)

// handleAggStatus GET /api/status — 聚合系統狀態（鏈＋錢包＋交易所＋獎勵池＋L2＋橋）。
// 供管理儀表板與健康檢查一覽；各子系統個別故障不阻斷整體（錯誤以欄位回傳）。
func (s *RPCServer) handleAggStatus(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{
		"ok":         true,
		"chain":      s.node.GetStatus(),
		"wallet":     map[string]any{},
		"exchange":   map[string]any{},
		"rewardpool": map[string]any{},
		"tiusd":      map[string]any{},
		"community": map[string]string{
			"telegram": os.Getenv("TAC_COMMUNITY_TELEGRAM"),
			"x":        os.Getenv("TAC_COMMUNITY_X"),
			"discord":  os.Getenv("TAC_COMMUNITY_DISCORD"),
			"site":     os.Getenv("TAC_COMMUNITY_SITE"),
		},
	}
	// 鏈上錢包：節點地址資產。
	if s.node.walletSvc != nil {
		if wv, err := s.node.walletSvc.Balance(s.node.nodeAddress); err == nil {
			out["wallet"] = map[string]any{
				"address": s.node.nodeAddress,
				"tacm":    wv.TACmBalance.String(),
				"tiusd":   wv.TiUSDBalance,
				"usdt":    wv.USDTBalance,
			}
		}
		if t, err := s.node.walletSvc.TiUSDSummary(); err == nil {
			out["tiusd"] = map[string]any{"supply": t.Supply, "total_minted": t.TotalMinted, "total_burned": t.TotalBurned}
		}
		if p, err := s.node.walletSvc.Balance(wallet.RewardPoolAddr); err == nil {
			out["rewardpool"] = map[string]any{"tacm": p.TACmBalance.String(), "share_bp": wallet.PoolShareBps}
		}
	}
	// 交易所：總資產＋手續費帳戶。
	if s.node.exchangeSvc != nil {
		ex := map[string]any{"fee_account": []map[string]any{}}
		if fb, err := s.node.exchangeSvc.Balances(exchange.FeeUID); err == nil {
			for _, b := range fb {
				ex["fee_account"] = append(ex["fee_account"].([]map[string]any), map[string]any{
					"asset": string(b.Asset), "avail": b.AvailStr,
				})
			}
		}
		out["exchange"] = ex
	}
	// L2 / 橋（可選模組）。
	if l2 := s.node.L2(); l2 != nil {
		if st, err := l2.Status(); err == nil {
			out["l2"] = st
		}
	}
	if b := s.node.bridge; b != nil {
		if st, err := b.Stats(); err == nil {
			out["bridge"] = st
		}
	}
	writeJSON(w, http.StatusOK, out)
}

var _ = json.Marshal // 保留 json import
