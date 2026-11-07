package chaindb

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// ErrMempoolUnderpriced 同 sender+nonce 已有更高手續費交易（Ethereum RBF 語義）：
// 新交易手續費未高於既有交易時拒絕替換。
var ErrMempoolUnderpriced = errors.New("chaindb: replacement transaction underpriced")

// rbfCheck 檢查並替換同 from+nonce 的既有交易（Ethereum RBF）：
//   - 無既有交易 → 可插入（回 true, nil）
//   - 新 fee > 舊 fee → 刪除舊交易、可插入（回 true, nil）
//   - 新 fee ≤ 舊 fee → 拒絕（回 false, ErrMempoolUnderpriced）
func (c *ChainDB) rbfCheck(q *sql.Tx, from string, nonce int64, fee float64) (bool, error) {
	var oldHash string
	var oldFee float64
	err := q.QueryRow(`
		SELECT tx_hash, CAST(fee AS REAL) FROM mempool
		WHERE json_extract(payload, '$.from') = ?
		  AND CAST(json_extract(payload, '$.nonce') AS INTEGER) = ?`,
		from, nonce).Scan(&oldHash, &oldFee)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if fee > oldFee {
		if _, err := q.Exec("DELETE FROM mempool WHERE tx_hash = ?", oldHash); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, ErrMempoolUnderpriced
}

// AddMempoolTx 將交易加入內存池：先淘汰過期交易，再寫入；
// 同 sender+nonce 已有交易時依手續費做 RBF 替換（高 fee 取代低 fee）；
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
	// M161-RBF：同 sender+nonce 高 fee 替換低 fee；低 fee 拒絕（eth_sendRawTransaction 回 underpriced）。
	from := getString(payload, "from")
	nonce := getInt64(payload, "nonce")
	f, ferr := strconv.ParseFloat(fee, 64)
	if from != "" && ferr == nil {
		ok, rerr := c.rbfCheck(tx, from, nonce, f)
		if rerr != nil {
			return rerr
		}
		if !ok {
			return ErrMempoolUnderpriced
		}
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
		// M161-RBF：同 sender+nonce 高 fee 替換低 fee（批次內與池內既有交易均適用）。
		from := getString(e.Payload, "from")
		nonce := getInt64(e.Payload, "nonce")
		f, ferr := strconv.ParseFloat(e.Fee, 64)
		if from != "" && ferr == nil {
			ok, rerr := c.rbfCheck(tx, from, nonce, f)
			if rerr != nil {
				return rerr
			}
			if !ok {
				return ErrMempoolUnderpriced
			}
		}
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

// GetMempoolTx 查詢單筆內存池交易（pending）；不存在回 (nil, nil)。
// 供 eth_getTransactionByHash 回傳 pending 交易（以太坊慣例：已提交未打包仍可查詢）。
func (c *ChainDB) GetMempoolTx(txHash string) (*MempoolPendingTx, error) {
	row := c.db.QueryRow(
		"SELECT tx_hash, payload, signature, fee, received_at FROM mempool WHERE tx_hash = ?",
		txHash)
	var m MempoolPendingTx
	if err := row.Scan(&m.TxHash, &m.Payload, &m.Signature, &m.Fee, &m.ReceivedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("chaindb: 查內存池交易失敗: %w", err)
	}
	return &m, nil
}

// MempoolPendingTx 內存池單筆交易（pending，eth_getTransactionByHash 用）。
type MempoolPendingTx struct {
	TxHash     string  `json:"tx_hash"`
	Payload    string  `json:"payload"`
	Signature  string  `json:"signature"`
	Fee        float64 `json:"fee"`
	ReceivedAt int64   `json:"received_at"`
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
// 只保留 fee 最高者；地址之間按「隊首有效小費」降序輪流（預設 PriorityTip）。
// 輸出順序即為區塊打包順序。
//
// 向後相容包裝：以 baseFee=0、預設策略委派至 GetMempoolPendingSorted。
// legacy 交易（無 max_fee/priority_fee）的有效小費即總 fee，故舊呼叫方
// （eth_filter pending RPC、既有測試）行為完全不變。
func (c *ChainDB) GetMempoolPending(limit int) ([]map[string]any, error) {
	return c.GetMempoolPendingSorted(limit, 0, PackPriorityTip)
}

// GetMempoolPendingSorted 是可打包佇列的策略化版本（MEV/優先費打包排序）。
//
// 與 GetMempoolPending 相同的佇列構造語義（逐字不變）：
//   - 每地址從鏈上 nonce（GetNonce）起連續取，nonce gap 之後截斷；
//   - 同一 nonce 多筆只保留「總 fee 最高」者（feeOf 覆蓋）；
//
// 唯一差異：地址間「輪流出一筆」的選擇鍵由「隊首總 fee」改為
// 「隊首交易在給定 baseFee 與 strategy 下的有效小費／策略排序鍵」，
// 並附確定性 tie-break（鍵相同→received_at 早→tx_hash 字典序），
// 保證所有節點打包順序一致（共識確定性）。
//
// baseFee 為本塊 EIP-1559 基礎價（TACm/gas）；legacy 交易與 baseFee 無關。
func (c *ChainDB) GetMempoolPendingSorted(limit int, baseFee float64, strategy PackStrategy) ([]map[string]any, error) {
	rows, err := c.db.Query(
		"SELECT tx_hash, payload, signature, received_at FROM mempool ORDER BY CAST(fee AS REAL) DESC")
	if err != nil {
		return nil, fmt.Errorf("chaindb: 讀內存池失敗: %w", err)
	}
	defer rows.Close()

	// 按地址 → nonce → 交易（同 nonce 高 fee 覆蓋低 fee）。
	type memRow struct {
		tx   map[string]any
		recv int64
	}
	byAddr := make(map[string]map[int64]memRow)
	for rows.Next() {
		var txHash, payload, signature string
		var recv int64
		if err := rows.Scan(&txHash, &payload, &signature, &recv); err != nil {
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
			m = make(map[int64]memRow)
			byAddr[from] = m
		}
		cur, ok := m[nonce]
		if !ok || feeOf(cur.tx) < feeOf(tx) {
			m[nonce] = memRow{tx: tx, recv: recv}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 每地址：從鏈上 nonce 起連續構建可執行列表（gap 截斷）。
	var queues [][]memRow
	for addr, m := range byAddr {
		start := c.GetNonce(addr)
		var q []memRow
		for n := start; ; n++ {
			r, ok := m[n]
			if !ok {
				break // nonce gap：其後交易暫不可打包
			}
			q = append(q, r)
		}
		if len(q) > 0 {
			queues = append(queues, q)
		}
	}

	// 地址輪流：每次取「隊首排序鍵最佳」的地址出一筆（確定性 tie-break）。
	out := make([]map[string]any, 0, limit)
	for len(queues) > 0 && len(out) < limit {
		best := 0
		bestKey := sortKeyFor(queues[best][0].tx, queues[best][0].recv, baseFee, strategy)
		for i := 1; i < len(queues); i++ {
			k := sortKeyFor(queues[i][0].tx, queues[i][0].recv, baseFee, strategy)
			if k.betterThan(bestKey) {
				best, bestKey = i, k
			}
		}
		out = append(out, queues[best][0].tx)
		queues[best] = queues[best][1:]
		if len(queues[best]) == 0 {
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
	case json.Number:
		n, err := v.Int64()
		if err == nil {
			return n
		}
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
