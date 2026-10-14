package node

import (
	"fmt"
	"strconv"
	"time"
)

// DailyEarning 單日收益（鏈上 coinbase:miner 聚合）。
type DailyEarning struct {
	Date    string  `json:"date"`    // YYYY-MM-DD（UTC+8）
	Earned  float64 `json:"earned"`  // 當日 coinbase:miner 收益（TACm）
	Count   int     `json:"count"`   // 當日瓜分筆數
	Hash    float64 `json:"hash"`    // 當日平均算力（hb:on 聲明值）
	Samples int     `json:"samples"` // 當日算力採樣塊數
}

// dayKey 將 unix 秒轉為 UTC+8 日期字串。
func dayKey(ts int64) string {
	return time.Unix(ts, 0).In(time.FixedZone("CST", 8*3600)).Format("2006-01-02")
}

// MinerEarningsDaily 回傳指定地址近 days 日（含今天）的鏈上 coinbase:miner 收益明細。
// 鏈上事實：任何節點同步同一條鏈結果一致。
func (n *Node) MinerEarningsDaily(addr string, days int) ([]DailyEarning, error) {
	if days <= 0 || days > 90 {
		days = 7
	}
	agg := map[string]*DailyEarning{}
	txs, err := n.db.GetCoinbaseTransactions()
	if err != nil {
		return nil, fmt.Errorf("node: 讀取 coinbase 交易失敗: %w", err)
	}
	now := time.Now().In(time.FixedZone("CST", 8*3600))
	cutoff := now.AddDate(0, 0, -(days - 1))
	for _, tx := range txs {
		if tx == nil || tx.ToAddr != addr || tx.Memo != "coinbase:miner" {
			continue
		}
		amt, err := strconv.ParseFloat(tx.Amount, 64)
		if err != nil {
			continue
		}
		d := dayKey(tx.Ts)
		if d < cutoff.Format("2006-01-02") {
			continue
		}
		e, ok := agg[d]
		if !ok {
			e = &DailyEarning{Date: d}
			agg[d] = e
		}
		e.Earned += amt
		e.Count++
	}
	out := make([]DailyEarning, 0, days)
	for i := days - 1; i >= 0; i-- {
		d := now.AddDate(0, 0, -i).Format("2006-01-02")
		if e, ok := agg[d]; ok {
			e.Earned = roundCoinbase(e.Earned)
			out = append(out, *e)
		} else {
			out = append(out, DailyEarning{Date: d})
		}
	}
	return out, nil
}

// MinerHashrateDaily 回傳指定地址近 days 日（含今天）的每日平均算力（hb:on 聲明值）。
// 由鏈上心跳交易重放：每日對該地址所有 hb:on 聲明算力取平均（未開機日為 0）。
func (n *Node) MinerHashrateDaily(addr string, days int) ([]DailyEarning, error) {
	if days <= 0 || days > 90 {
		days = 7
	}
	type dayAgg struct {
		sum   int64
		n     int
		onCnt int
	}
	agg := map[string]*dayAgg{}
	now := time.Now().In(time.FixedZone("CST", 8*3600))
	cutoff := now.AddDate(0, 0, -(days - 1))
	tip := n.db.GetTipHeight()
	for h := int64(1); h <= tip; h++ {
		txs, err := n.db.GetTransactionsByBlock(h)
		if err != nil {
			return nil, fmt.Errorf("node: 掃區塊 %d 失敗: %w", h, err)
		}
		for _, tx := range txs {
			if tx == nil || tx.ToAddr != addr {
				continue
			}
			on, hr, ok := parseHeartbeatMemo(tx.Memo)
			if !ok {
				continue
			}
			d := dayKey(tx.Ts)
			if d < cutoff.Format("2006-01-02") {
				continue
			}
			a, ok2 := agg[d]
			if !ok2 {
				a = &dayAgg{}
				agg[d] = a
			}
			a.n++
			if on {
				a.sum += hr
				a.onCnt++
			}
		}
	}
	out := make([]DailyEarning, 0, days)
	for i := days - 1; i >= 0; i-- {
		d := now.AddDate(0, 0, -i).Format("2006-01-02")
		e := DailyEarning{Date: d}
		if a, ok := agg[d]; ok && a.n > 0 {
			e.Samples = a.n
			if a.onCnt > 0 {
				e.Hash = float64(a.sum) / float64(a.onCnt)
			}
		}
		out = append(out, e)
	}
	return out, nil
}
