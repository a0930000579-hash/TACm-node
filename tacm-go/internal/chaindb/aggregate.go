package chaindb

import "fmt"

// CoinbaseTotals 為 coinbase 交易的全鏈標量聚合（單行），由 SQLite 直接聚合，
// 取代把全部 coinbase 列載入 Go 再累加（鏈高時數十萬物件、GC/鎖開銷極大）。
type CoinbaseTotals struct {
	Count int     // coinbase 交易總筆數
	Total float64 // 全鏈 coinbase 總額（鏈上真實總產出）
	Pool  float64 // memo='coinbase:pool' 累計（獎勵池，15%）
	Node  float64 // memo='coinbase:node' 累計（節點獎勵，45%）
}

// GetCoinbaseTotals 以單條 SQL 聚合 coinbase 總額與 pool/node 份額。
// 篩選條件與 GetCoinbaseTransactions 完全一致（memo='coinbase' 或前綴 'coinbase:'）。
func (c *ChainDB) GetCoinbaseTotals() (CoinbaseTotals, error) {
	const q = `SELECT
		COUNT(*),
		COALESCE(SUM(CAST(amount AS REAL)), 0),
		COALESCE(SUM(CASE WHEN memo = 'coinbase:pool' THEN CAST(amount AS REAL) ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN memo = 'coinbase:node' THEN CAST(amount AS REAL) ELSE 0 END), 0)
	FROM transactions
	WHERE memo = 'coinbase' OR memo LIKE 'coinbase:%'`
	var t CoinbaseTotals
	if err := c.db.QueryRow(q).Scan(&t.Count, &t.Total, &t.Pool, &t.Node); err != nil {
		return t, fmt.Errorf("chaindb: coinbase 聚合失敗: %w", err)
	}
	return t, nil
}

// GetCoinbaseTotalsSince 僅聚合 block_height > minHeight 的 coinbase 增量（走 idx_tx_block），
// 供鏈頂推進時增量累加，避免每次對全鏈 coinbase 列重掃。篩選口徑與 GetCoinbaseTotals 相同。
func (c *ChainDB) GetCoinbaseTotalsSince(minHeight int64) (CoinbaseTotals, error) {
	const q = `SELECT
		COUNT(*),
		COALESCE(SUM(CAST(amount AS REAL)), 0),
		COALESCE(SUM(CASE WHEN memo = 'coinbase:pool' THEN CAST(amount AS REAL) ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN memo = 'coinbase:node' THEN CAST(amount AS REAL) ELSE 0 END), 0)
	FROM transactions
	WHERE (memo = 'coinbase' OR memo LIKE 'coinbase:%') AND block_height > ?`
	var t CoinbaseTotals
	if err := c.db.QueryRow(q, minHeight).Scan(&t.Count, &t.Total, &t.Pool, &t.Node); err != nil {
		return t, fmt.Errorf("chaindb: coinbase 增量聚合失敗: %w", err)
	}
	return t, nil
}

// GetCoinbaseMinerAggregates 由 SQLite 分組聚合各地址 coinbase:miner 累計收益與筆數，
// 僅回傳不重複的礦工地址（遠少於 coinbase 總列數），取代 Go 端逐列掃描。
func (c *ChainDB) GetCoinbaseMinerAggregates() (map[string]float64, map[string]int, error) {
	const q = `SELECT to_addr, COALESCE(SUM(CAST(amount AS REAL)), 0), COUNT(*)
		FROM transactions
		WHERE memo = 'coinbase:miner'
		GROUP BY to_addr`
	rows, err := c.db.Query(q)
	if err != nil {
		return nil, nil, fmt.Errorf("chaindb: coinbase 礦工聚合失敗: %w", err)
	}
	defer rows.Close()
	miners := map[string]float64{}
	counts := map[string]int{}
	for rows.Next() {
		var addr string
		var amt float64
		var n int
		if err := rows.Scan(&addr, &amt, &n); err != nil {
			return nil, nil, fmt.Errorf("chaindb: coinbase 礦工列掃描失敗: %w", err)
		}
		miners[addr] = amt
		counts[addr] = n
	}
	return miners, counts, rows.Err()
}
