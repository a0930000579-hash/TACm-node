package chaindb

import (
	"database/sql"
	"fmt"
	"strings"
)

// LogRow 為鏈上事件日誌記錄（logs 表），以以太 eth_getLogs 欄位語義對外輸出。
type LogRow struct {
	ID          int64    `json:"-"`
	TxHash      string   `json:"transaction_hash"`
	BlockHeight int64    `json:"block_number"`
	TxIndex     int      `json:"transaction_index"`
	LogIndex    int      `json:"log_index"`
	Address     string   `json:"address"`
	Topics      []string `json:"topics"`
	Data        string   `json:"data"`
}

// LogFilter 為事件日誌查詢條件；空值欄位不參與過濾。
type LogFilter struct {
	FromBlock int64
	ToBlock   int64
	TxHash    string
	Address   string   // 0x 前綴 EVM 地址（logs 表存 0x 前綴）
	Addresses []string // M128：多地址（任一匹配；與 Address 併用）
	Topic0    string   // 0x 前綴事件簽名
	Topics    [][]string // M128：多層 topics（層內任一 OR、跨層 AND；與 Topic0 併用）
	Limit     int
}

// InsertLogs 寫入一筆交易的完整事件日誌（合約執行後呼叫）。
func (c *ChainDB) InsertLogs(txHash string, blockHeight int64, txIndex int, rows []LogRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := c.db.Begin()
	if err != nil {
		return fmt.Errorf("chaindb: 開啟日誌事務失敗: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for i := range rows {
		l := &rows[i]
		t0, t1, t2, t3 := "", "", "", ""
		if len(l.Topics) > 0 {
			t0 = l.Topics[0]
		}
		if len(l.Topics) > 1 {
			t1 = l.Topics[1]
		}
		if len(l.Topics) > 2 {
			t2 = l.Topics[2]
		}
		if len(l.Topics) > 3 {
			t3 = l.Topics[3]
		}
		if _, err := tx.Exec(`
			INSERT INTO logs
			(tx_hash, block_height, tx_index, log_index, address, topic0, topic1, topic2, topic3, data)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			txHash, blockHeight, txIndex, i, l.Address, t0, t1, t2, t3, l.Data); err != nil {
			return fmt.Errorf("chaindb: 寫入日誌失敗: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("chaindb: 提交日誌事務失敗: %w", err)
	}
	return nil
}

// GetLogs 依條件查詢事件日誌（按區塊/索引升序）。
func (c *ChainDB) GetLogs(f LogFilter) ([]LogRow, error) {
	where := []string{"1=1"}
	args := []any{}
	if f.FromBlock > 0 {
		where = append(where, "block_height >= ?")
		args = append(args, f.FromBlock)
	}
	if f.ToBlock > 0 {
		where = append(where, "block_height <= ?")
		args = append(args, f.ToBlock)
	}
	if f.TxHash != "" {
		where = append(where, "tx_hash = ?")
		args = append(args, f.TxHash)
	}
	addrSet := map[string]bool{}
	if f.Address != "" {
		addrSet[f.Address] = true
	}
	for _, a := range f.Addresses {
		if a != "" {
			addrSet[a] = true
		}
	}
	if len(addrSet) == 1 {
		for a := range addrSet {
			where = append(where, "LOWER(address) = LOWER(?)")
			args = append(args, a)
		}
	} else if len(addrSet) > 1 {
		in := make([]string, 0, len(addrSet))
		for a := range addrSet {
			in = append(in, "LOWER(?)")
			args = append(args, a)
		}
		where = append(where, "LOWER(address) IN ("+strings.Join(in, ",")+")")
	}
	// topics 層 0：候選收窄用（任一名義值 IN），完整多層匹配在記憶體完成。
	topic0Set := map[string]bool{}
	if f.Topic0 != "" {
		topic0Set[f.Topic0] = true
	}
	if len(f.Topics) > 0 {
		for _, v := range f.Topics[0] {
			if v != "" {
				topic0Set[v] = true
			}
		}
	}
	if len(topic0Set) == 1 {
		for t := range topic0Set {
			where = append(where, "LOWER(topic0) = LOWER(?)")
			args = append(args, t)
		}
	} else if len(topic0Set) > 1 {
		in := make([]string, 0, len(topic0Set))
		for t := range topic0Set {
			in = append(in, "LOWER(?)")
			args = append(args, t)
		}
		where = append(where, "LOWER(topic0) IN ("+strings.Join(in, ",")+")")
	}
	limit := f.Limit
	if limit <= 0 || limit > 10000 {
		limit = 10000
	}
	args = append(args, limit)
	rows, err := c.db.Query(`
		SELECT id, tx_hash, block_height, tx_index, log_index, address,
		       topic0, topic1, topic2, topic3, data
		FROM logs WHERE `+strings.Join(where, " AND ")+`
		ORDER BY block_height ASC, tx_index ASC, log_index ASC LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("chaindb: 查日誌失敗: %w", err)
	}
	defer rows.Close()
	var out []LogRow
	for rows.Next() {
		var (
			l                   LogRow
			addr                string
			t0, t1, t2, t3, dt  sql.NullString
		)
		if err := rows.Scan(&l.ID, &l.TxHash, &l.BlockHeight, &l.TxIndex, &l.LogIndex,
			&addr, &t0, &t1, &t2, &t3, &dt); err != nil {
			return nil, fmt.Errorf("chaindb: 掃描日誌失敗: %w", err)
		}
		l.Address = addr
		l.Topics = []string{}
		if t0.Valid {
			l.Topics = append(l.Topics, t0.String)
		}
		if t1.Valid {
			l.Topics = append(l.Topics, t1.String)
		}
		if t2.Valid {
			l.Topics = append(l.Topics, t2.String)
		}
		if t3.Valid {
			l.Topics = append(l.Topics, t3.String)
		}
		l.Data = dt.String
		if len(f.Topics) > 1 && !matchTopics(l.Topics, f.Topics) {
			continue
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// UpdateTxGasUsed 回寫合約交易實際消耗的 gas（收據用）。
func (c *ChainDB) UpdateTxGasUsed(txHash string, gasUsed int64) error {
	if _, err := c.db.Exec("UPDATE transactions SET gas_used = ? WHERE tx_hash = ?", gasUsed, txHash); err != nil {
		return fmt.Errorf("chaindb: 更新交易 gas_used 失敗: %w", err)
	}
	if c.cache != nil {
		c.cache.invalidateTx(txHash)
	}
	return nil
}


// matchTopics 依標準以太 topics 語意匹配：每一層（索引 i）只與 topics[i] 對應，
// 層內多值為 OR、跨層為 AND；nil 層表示不限制該位置。
func matchTopics(logTopics []string, filter [][]string) bool {
	for i, layer := range filter {
		if i == 0 {
			continue // 第 0 層已由 SQL topic0 IN 收窄
		}
		if i >= len(logTopics) {
			return false
		}
		if len(layer) == 0 {
			continue
		}
		matched := false
		for _, want := range layer {
			if want == "" {
				continue
			}
			if strings.EqualFold(logTopics[i], want) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
