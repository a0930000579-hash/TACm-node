package node

// M68 鏈上真實聚合：掃鏈上全部 coinbase 交易計算「總產出／礦工收益／pool 挹注」。
//
// 取代本地 chain_stats / wallet Ledger 快照作為「我的收益」「總產出」的口徑：
// 任何節點只要同步同一條鏈，掃描結果完全一致（鏈上事實），徹底鎖死
// 「收益與獎勵池數字必須代表鏈上真實、A+B 不得大於實際總產量」。

import (
	"math"
	"strconv"
	"sync"
	"time"
)

// aggCache* M146：鏈上 coinbase 聚合快取（5 秒 TTL、tip 為鍵）。
// /api/miner/earnings 與 /api/chain/stats 每次呼叫都全掃 coinbase 表（鏈高 12 萬時
// 數十萬行），手機端挖礦頁輪詢明顯卡頓；同 tip 下短 TTL 快取讓輪詢秒回應。
var (
	aggCacheMu  sync.Mutex
	aggCache    CoinbaseAggregate
	aggCacheTip int64
	aggCacheAt  time.Time
)

// roundCoinbase 把累加值收斂到 1e-4（coinbase 金額精度），避免浮點累加
// 出現 73869.99999999377 而非 73870 的顯示，確保「總產出／收益數字精確代表鏈上真實」。
func roundCoinbase(v float64) float64 {
	return math.Round(v*1e4) / 1e4
}

// CoinbaseAggregate 鏈上 coinbase 聚合結果（記憶存檔點：M68 核心結構）。
type CoinbaseAggregate struct {
	Total      float64            // 全鏈 coinbase 總額（鏈上真實總產出）
	Pool       float64            // coinbase:pool 累計（獎勵池挹注來源，15%）
	Node       float64            // coinbase:node 累計（節點獎勵，45%，M70）
	Miners     map[string]float64 // 各地址 coinbase:miner 累計（鏈上真實「我的收益」）
	MinerCount map[string]int     // 各地址 coinbase:miner 交易筆數
	Count      int                // coinbase 交易總筆數
	TipHeight  int64              // 掃描時的鏈頂（結果對應的鏈高）
}

// cloneAggregate 深拷貝聚合結果（快取回傳副本，避免呼叫方修改共享 map）。
func cloneAggregate(a CoinbaseAggregate) CoinbaseAggregate {
	out := CoinbaseAggregate{
		Total: a.Total, Pool: a.Pool, Node: a.Node,
		Count: a.Count, TipHeight: a.TipHeight,
		Miners:     make(map[string]float64, len(a.Miners)),
		MinerCount: make(map[string]int, len(a.MinerCount)),
	}
	for k, v := range a.Miners {
		out.Miners[k] = v
	}
	for k, v := range a.MinerCount {
		out.MinerCount[k] = v
	}
	return out
}

// OnChainCoinbaseAggregate 掃鏈上全部 coinbase 交易並聚合（M68）。
// M146：同 tip 且 5 秒內重複查詢直接回快取副本；鏈推進後自動重掃。
func (n *Node) OnChainCoinbaseAggregate() (CoinbaseAggregate, error) {
	tip := n.db.GetTipHeight()
	aggCacheMu.Lock()
	defer aggCacheMu.Unlock()
	if aggCacheTip == tip && time.Since(aggCacheAt) < 5*time.Second {
		return cloneAggregate(aggCache), nil
	}
	out := CoinbaseAggregate{Miners: map[string]float64{}, MinerCount: map[string]int{}}
	txs, err := n.db.GetCoinbaseTransactions()
	if err != nil {
		return out, err
	}
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		amt, err := strconv.ParseFloat(tx.Amount, 64)
		if err != nil {
			continue
		}
		out.Count++
		out.Total += amt
		switch {
		case tx.Memo == "coinbase:pool":
			out.Pool += amt
		case tx.Memo == "coinbase:node":
			out.Node += amt
		case tx.Memo == "coinbase:miner":
			out.Miners[tx.ToAddr] += amt
			out.MinerCount[tx.ToAddr]++
			// coinbase:reserve 與 coinbase（舊式）不計入礦工收益，仍屬總產出。
		}
	}
	out.TipHeight = n.db.GetTipHeight()
	// 收斂浮點累加誤差，確保顯示精確（M68.1）。
	out.Total = roundCoinbase(out.Total)
	out.Pool = roundCoinbase(out.Pool)
	out.Node = roundCoinbase(out.Node)
	for a := range out.Miners {
		out.Miners[a] = roundCoinbase(out.Miners[a])
	}
	aggCache = out
	aggCacheTip = tip
	aggCacheAt = time.Now()
	return out, nil
}

// MinerEarnedOnChain 回傳指定地址的鏈上 coinbase:miner 累計收益（TACm 浮點）與筆數。
// 鏈上事實：任何節點同步同一條鏈結果一致（M68）。
func (n *Node) MinerEarnedOnChain(addr string) (float64, int, error) {
	agg, err := n.OnChainCoinbaseAggregate()
	if err != nil {
		return 0, 0, err
	}
	return agg.Miners[addr], agg.MinerCount[addr], nil
}
