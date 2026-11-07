package node

// M68 鏈上真實聚合：掃鏈上全部 coinbase 交易計算「總產出／礦工收益／pool 挹注」。
//
// 取代本地 chain_stats / wallet Ledger 快照作為「我的收益」「總產出」的口徑：
// 任何節點只要同步同一條鏈，掃描結果完全一致（鏈上事實），徹底鎖死
// 「收益與獎勵池數字必須代表鏈上真實、A+B 不得大於實際總產量」。

import (
	"math"
	"sync"

	"tacm/internal/chaindb"
)

// aggCache*：鏈上 coinbase 聚合快取。同 tip 直接回快取；鏈頂推進時只對新塊做增量
// 聚合（GetCoinbaseTotalsSince，走 block_height 索引）累加，每 aggReanchorEvery 塊或
// 重組/首次時做一次全量聚合重新對齊，避免每次審計對全鏈 coinbase 列重掃。
var (
	aggCacheMu       sync.Mutex
	aggCache         CoinbaseAggregate
	aggCacheTip      int64
	aggInitialized   bool
	aggLastFullTip   int64
	aggReanchorEvery int64 = 5000
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

// OnChainCoinbaseAggregate 聚合鏈上 coinbase 交易（M68）。同 tip 回快取；鏈頂推進做
// 增量累加；首次/重組/每 aggReanchorEvery 塊做全量重新對齊，兼顧效能與口徑正確。
func (n *Node) OnChainCoinbaseAggregate() (CoinbaseAggregate, error) {
	tip := n.db.GetTipHeight()
	aggCacheMu.Lock()
	defer aggCacheMu.Unlock()
	if aggInitialized && aggCacheTip == tip {
		return cloneAggregate(aggCache), nil
	}
	// 全量重新對齊條件：首次、鏈回滾（重組）、或距上次全量已達週期。
	full := !aggInitialized || tip < aggCacheTip || tip-aggLastFullTip >= aggReanchorEvery
	var tot chaindb.CoinbaseTotals
	if full {
		t, err := n.db.GetCoinbaseTotals()
		if err != nil {
			return CoinbaseAggregate{Miners: map[string]float64{}, MinerCount: map[string]int{}}, err
		}
		tot = t
		aggLastFullTip = tip
	} else {
		inc, err := n.db.GetCoinbaseTotalsSince(aggCacheTip)
		if err != nil {
			return CoinbaseAggregate{Miners: map[string]float64{}, MinerCount: map[string]int{}}, err
		}
		tot = chaindb.CoinbaseTotals{
			Count: aggCache.Count + inc.Count,
			Total: aggCache.Total + inc.Total,
			Pool:  aggCache.Pool + inc.Pool,
			Node:  aggCache.Node + inc.Node,
		}
	}
	out := CoinbaseAggregate{Miners: map[string]float64{}, MinerCount: map[string]int{}}
	out.Count = tot.Count
	out.Total = roundCoinbase(tot.Total)
	out.Pool = roundCoinbase(tot.Pool)
	out.Node = roundCoinbase(tot.Node)
	out.TipHeight = tip
	aggCache = out
	aggCacheTip = tip
	aggInitialized = true
	return out, nil
}

// MinerEarnedOnChain 回傳指定地址的鏈上 coinbase:miner 累計收益（TACm 浮點）與筆數。
// 鏈上事實：任何節點同步同一條鏈結果一致（M68）。礦工分組屬按需查詢，不進審計熱路徑。
func (n *Node) MinerEarnedOnChain(addr string) (float64, int, error) {
	miners, counts, err := n.db.GetCoinbaseMinerAggregates()
	if err != nil {
		return 0, 0, err
	}
	return roundCoinbase(miners[addr]), counts[addr], nil
}
