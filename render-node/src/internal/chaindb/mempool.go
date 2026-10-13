package chaindb

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// AddMempoolTx 將交易加入內存池：先淘汰過期交易，再寫入；
// 容量超限時淘汰手續費最低的交易。
func (c *ChainDB) AddMempoolTx(txHash string, payload map[string]any, signature, fee string) error {
	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	cutoff := nowUnix() - MempoolExpireSec
	if _, err := tx.Exec("DELETE FROM mempool WHERE received_at < ?", cutoff); err != nil {
		return err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("chaindb: 序列化內存池交易失敗: %w", err)
	}
	if _, err := tx.Exec(`
		INSERT OR REPLACE INTO mempool (tx_hash, payload, signature, received_at, fee)
		VALUES (?, ?, ?, ?, ?)`, txHash, string(raw), signature, nowUnix(), fee); err != nil {
		return err
	}

	var count int64
	if err := tx.QueryRow("SELECT COUNT(*) FROM mempool").Scan(&count); err != nil {
		return err
	}
	if count > MaxMempoolSize {
		evict := count - MaxMempoolSize
		if _, err := tx.Exec(`
			DELETE FROM mempool WHERE tx_hash IN (
			    SELECT tx_hash FROM mempool ORDER BY CAST(fee AS REAL) ASC LIMIT ?)`,
			evict); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// MempoolEntry 批量落池的一筆交易（M119）。
type MempoolEntry struct {
	TxHash    string
	Payload   map[string]any
	Signature string
	Fee       string
}

// AddMempoolTxs M119：批量加入內存池——單一事務寫入多筆＋一次過期淘汰＋一次容量驅逐。
// 消除 SubmitTransactions 批量提交時每筆一次 BEGIN/COMMIT 的 SQLite fsync 成本
// （與主流鏈交易池批量寫入同級）。
func (c *ChainDB) AddMempoolTxs(entries []MempoolEntry) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	cutoff := nowUnix() - MempoolExpireSec
	if _, err := tx.Exec("DELETE FROM mempool WHERE received_at < ?", cutoff); err != nil {
		return err
	}
	now := nowUnix()
	for _, e := range entries {
		raw, merr := json.Marshal(e.Payload)
		if merr != nil {
			return fmt.Errorf("chaindb: 序列化內存池交易失敗: %w", merr)
		}
		if _, err := tx.Exec(`
			INSERT OR REPLACE INTO mempool (tx_hash, payload, signature, received_at, fee)
			VALUES (?, ?, ?, ?, ?)`, e.TxHash, string(raw), e.Signature, now, e.Fee); err != nil {
			return err
		}
	}
	var count int64
	if err := tx.QueryRow("SELECT COUNT(*) FROM mempool").Scan(&count); err != nil {
		return err
	}
	if count > MaxMempoolSize {
		evict := count - MaxMempoolSize
		if _, err := tx.Exec(`
			DELETE FROM mempool WHERE tx_hash IN (
			    SELECT tx_hash FROM mempool ORDER BY CAST(fee AS REAL) ASC LIMIT ?)`,
			evict); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetMempool 返回按手續費降序的內存池交易，每筆補上 tx_hash 與 signature。
func (c *ChainDB) GetMempool(limit int) ([]map[string]any, error) {
	rows, err := c.db.Query(
		"SELECT tx_hash, payload, signature FROM mempool ORDER BY CAST(fee AS REAL) DESC LIMIT ?",
		limit)
	if err != nil {
		return nil, fmt.Errorf("chaindb: 讀內存池失敗: %w", err)
	}
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		var txHash, payload, signature string
		if err := rows.Scan(&txHash, &payload, &signature); err != nil {
			return nil, err
		}
		var tx map[string]any
		if err := json.Unmarshal([]byte(payload), &tx); err != nil {
			continue // 損壞記錄跳過
		}
		tx["tx_hash"] = txHash
		tx["signature"] = signature
		out = append(out, tx)
	}
	return out, rows.Err()
}

// RemoveMempoolTx 按哈希移除內存池交易。
func (c *ChainDB) RemoveMempoolTx(txHash string) error {
	_, err := c.db.Exec("DELETE FROM mempool WHERE tx_hash = ?", txHash)
	return err
}

// MempoolSize 返回內存池交易數。
func (c *ChainDB) MempoolSize() int64 {
	var n int64
	if err := c.db.QueryRow("SELECT COUNT(*) FROM mempool").Scan(&n); err != nil {
		return 0
	}
	return n
}

// MempoolTxs 返回內存池全部交易（txpool_status/content 用）：
// 每筆含哈希、from/to/amount/fee/nonce 與原始 payload。
func (c *ChainDB) MempoolTxs() ([]MempoolTx, error) {
	rows, err := c.db.Query("SELECT tx_hash, payload, fee FROM mempool ORDER BY received_at ASC")
	if err != nil {
		return nil, fmt.Errorf("chaindb: 查 mempool 失敗: %w", err)
	}
	defer rows.Close()

	var out []MempoolTx
	for rows.Next() {
		var m MempoolTx
		var raw string
		if err := rows.Scan(&m.TxHash, &raw, &m.Fee); err != nil {
			return nil, fmt.Errorf("chaindb: 讀 mempool 失敗: %w", err)
		}
		if err := json.Unmarshal([]byte(raw), &m.Payload); err != nil {
			continue // payload 損壞的單筆跳過，不影響其餘
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MempoolTx 為 mempool 明細行（txpool RPC 使用）。
type MempoolTx struct {
	TxHash  string         `json:"tx_hash"`
	Fee     string         `json:"fee"`
	Payload map[string]any `json:"payload"`
}

// ---- Peer ----

// UpsertPeer 登記或更新對等節點信息（標記為在線）。
func (c *ChainDB) UpsertPeer(nodeID, rpcURL, owner string, height int64) error {
	_, err := c.db.Exec(`
		INSERT OR REPLACE INTO peers (node_id, rpc_url, owner, last_seen, online, height)
		VALUES (?, ?, ?, ?, 1, ?)`, nodeID, rpcURL, owner, nowUnix(), height)
	if err != nil {
		return fmt.Errorf("chaindb: 更新 peer 失敗: %w", err)
	}
	return nil
}

// GetPeers 返回已知節點；onlineOnly 時僅返回在線節點。
func (c *ChainDB) GetPeers(onlineOnly bool) ([]*Peer, error) {
	q := "SELECT node_id, rpc_url, owner, last_seen, online, height FROM peers"
	if onlineOnly {
		q += " WHERE online = 1"
	}
	q += " ORDER BY last_seen DESC"
	rows, err := c.db.Query(q)
	if err != nil {
		return nil, fmt.Errorf("chaindb: 查 peers 失敗: %w", err)
	}
	defer rows.Close()

	var out []*Peer
	for rows.Next() {
		var p Peer
		if err := rows.Scan(&p.NodeID, &p.RPCURL, &p.Owner, &p.LastSeen,
			&p.Online, &p.Height); err != nil {
			return nil, err
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}

// ---- M85：以太坊式 pending 佇列（nonce 依序打包） ----

// GetMempoolPending 返回「可打包佇列」：每個地址從其鏈上 nonce 起連續取
// （nonce gap 之後的交易跳過，留待前序交易入塊後再打包）；同一 nonce 多筆
// 只保留 fee 最高者；地址之間按「隊首最高 fee」降序輪流，兼顧收費與公平。
// 輸出順序即為區塊打包順序（produceBlock 直接使用）。
func (c *ChainDB) GetMempoolPending(limit int) ([]map[string]any, error) {
	rows, err := c.db.Query(
		"SELECT tx_hash, payload, signature FROM mempool ORDER BY CAST(fee AS REAL) DESC")
	if err != nil {
		return nil, fmt.Errorf("chaindb: 讀內存池失敗: %w", err)
	}
	defer rows.Close()

	// 按地址 → nonce → 交易（同 nonce 高 fee 覆蓋低 fee）。
	byAddr := make(map[string]map[int64]map[string]any)
	for rows.Next() {
		var txHash, payload, signature string
		if err := rows.Scan(&txHash, &payload, &signature); err != nil {
			return nil, err
		}
		var tx map[string]any
		if err := json.Unmarshal([]byte(payload), &tx); err != nil {
			continue // 損壞記錄跳過
		}
		tx["tx_hash"] = txHash
		tx["signature"] = signature
		from := getString(tx, "from")
		nonce := getInt64(tx, "nonce")
		if from == "" {
			continue
		}
		m, ok := byAddr[from]
		if !ok {
			m = make(map[int64]map[string]any)
			byAddr[from] = m
		}
		cur, ok := m[nonce]
		if !ok || feeOf(cur) < feeOf(tx) {
			m[nonce] = tx
		}
	}

	// 每地址：從鏈上 nonce 起連續構建可執行列表（gap 截斷）。
	type addrQueue struct {
		addr  string
		start int64 // 該地址鏈上 nonce（GetNonce 含 pending 佔用，見 read.go）
		txs   []map[string]any
	}
	var queues []*addrQueue
	for addr, m := range byAddr {
		q := &addrQueue{addr: addr, start: c.GetNonce(addr)}
		n := q.start
		for {
			tx, ok := m[n]
			if !ok {
				break // nonce gap：其後交易暫不可打包
			}
			q.txs = append(q.txs, tx)
			n++
		}
		if len(q.txs) > 0 {
			queues = append(queues, q)
		}
	}

	// 地址輪流：每次取「隊首 fee 最高」的地址出一筆（公平＋高費優先）。
	out := make([]map[string]any, 0, limit)
	for len(queues) > 0 && len(out) < limit {
		best := 0
		for i := 1; i < len(queues); i++ {
			if feeOf(queues[i].txs[0]) > feeOf(queues[best].txs[0]) {
				best = i
			}
		}
		out = append(out, queues[best].txs[0])
		queues[best].txs = queues[best].txs[1:]
		if len(queues[best].txs) == 0 {
			queues = append(queues[:best], queues[best+1:]...)
		}
	}
	return out, nil
}

func getString(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

func getInt64(m map[string]any, k string) int64 {
	switch v := m[k].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case string:
		var n int64
		fmt.Sscanf(v, "%d", &n)
		return n
	}
	return 0
}

// feeOf 解析交易 fee（無效視為 0）。
func feeOf(m map[string]any) float64 {
	f, err := strconv.ParseFloat(getString(m, "fee"), 64)
	if err != nil {
		return 0
	}
	return f
}
