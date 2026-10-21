package chaindb

import (
	"strconv"
	"strings"
)

// NodeRewardStats 節點獎勵驗證資料——掃描最近 window 個區塊，
// 統計節點地址的 coinbase:node 入帳（節點獎勵）與 hb:on 心跳上鏈次數。
// 節點架設者可據此確認「是否真的收到獎勵」，即使單筆金額很小也能看到累計與最近入帳區塊。
type NodeRewardStats struct {
	RewardCount     int64  `json:"reward_count"`      // 近窗口 coinbase:node 入帳筆數
	RewardTotal     string `json:"reward_total"`      // 累計入帳（18 位小數字串）
	LastRewardBlock int64  `json:"last_reward_block"` // 最近一次入帳的區塊高度（0 = 尚未收到）
	HeartbeatCount  int64  `json:"heartbeat_count"`   // 近窗口 hb:on 心跳上鏈次數
	LastHeartbeat   int64  `json:"last_heartbeat"`    // 最近一次心跳區塊高度（0 = 無）
}

// NodeRewardStats 掃描 tip-window+1 .. tip 的區塊交易（窗口預設 200 塊）。
// 純鏈上重放：任何節點同步同一條鏈即得到完全一致的驗證結果。
func (c *ChainDB) NodeRewardStats(nodeAddr string, window int64) NodeRewardStats {
	var out NodeRewardStats
	var total float64
	if window <= 0 {
		window = 200
	}
	tip := c.GetTipHeight()
	start := tip - window + 1
	if start < 1 {
		start = 1
	}
	for h := start; h <= tip; h++ {
		txs, err := c.GetTransactionsByBlock(h)
		if err != nil {
			continue
		}
		for i := range txs {
			tx := txs[i]
			if tx.Memo == "coinbase:node" && tx.ToAddr == nodeAddr {
				amt, _ := strconv.ParseFloat(tx.Amount, 64)
				total += amt
				out.RewardCount++
				out.LastRewardBlock = h
			}
			if strings.HasPrefix(tx.Memo, "hb:on") && tx.FromAddr == nodeAddr {
				out.HeartbeatCount++
				out.LastHeartbeat = h
			}
		}
	}
	out.RewardTotal = FormatFloat(total)
	return out
}
