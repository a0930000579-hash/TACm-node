package chaindb

import (
	"database/sql"
	"fmt"
	"strings"
)

const txCols = `tx_hash, block_height, block_hash, tx_index, from_addr, to_addr,
	amount, fee, gas_limit, gas_used, max_fee, priority_fee, burned, nonce, ts, signature, pubkey, memo, status`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTransaction(s rowScanner) (*Transaction, error) {
	var (
		t                        Transaction
		from, to, memo, sig, pub sql.NullString
	)
	var gasLimit, gasUsed sql.NullInt64
	if err := s.Scan(&t.TxHash, &t.BlockHeight, &t.BlockHash, &t.TxIndex,
		&from, &to, &t.Amount, &t.Fee, &gasLimit, &gasUsed, &t.MaxFee, &t.PriorityFee,
		&t.Burned, &t.Nonce, &t.Ts, &sig, &pub, &memo, &t.Status); err != nil {
		return nil, err
	}
	t.GasLimit = gasLimit.Int64
	t.GasUsed = gasUsed.Int64
	t.FromAddr = from.String
	t.ToAddr = to.String
	t.Memo = memo.String
	t.Signature = sig.String
	t.Pubkey = pub.String
	return &t, nil
}

// GetTransaction 按哈希取交易；不存在返回 (nil, nil)。
func (c *ChainDB) GetTransaction(txHash string) (*Transaction, error) {
	row := c.db.QueryRow("SELECT "+txCols+" FROM transactions WHERE tx_hash = ?", txHash)
	t, err := scanTransaction(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("chaindb: 查交易失敗: %w", err)
	}
	if c.cache != nil {
		c.cache.putTx(txHash, t)
	}
	return t, nil
}

// GetTransactionsByAddress 返回與地址相關的交易（按高度/索引降序）。
func (c *ChainDB) GetTransactionsByAddress(address string, limit int) ([]*Transaction, error) {
	return c.GetTransactionsByAddressPage(address, 0, limit, "")
}

// GetTransactionsByAddressPage M88：分頁查地址交易；dir 可為 in（僅轉入）／out（僅轉出）／空（全部）。
func (c *ChainDB) GetTransactionsByAddressPage(address string, offset, limit int, dir string) ([]*Transaction, error) {
	where := "FROM transactions WHERE from_addr = ? OR to_addr = ?"
	args := []any{address, address}
	switch dir {
	case "in":
		where = "FROM transactions WHERE to_addr = ?"
		args = []any{address}
	case "out":
		where = "FROM transactions WHERE from_addr = ?"
		args = []any{address}
	}
	args = append(args, limit, offset)
	rows, err := c.db.Query("SELECT "+txCols+" "+
		where+" ORDER BY block_height DESC, tx_index DESC LIMIT ? OFFSET ?", args...)
	if err != nil {
		return nil, fmt.Errorf("chaindb: 按地址查交易失敗: %w", err)
	}
	defer rows.Close()
	return collectTransactions(rows)
}

// CountTransactionsByAddress 返回與地址相關的交易總數（dir=in|out|空，與
// GetTransactionsByAddressPage 同過濾語義），供 Explorer/錢包完整分頁。
func (c *ChainDB) CountTransactionsByAddress(address, dir string) (int64, error) {
	where := "WHERE from_addr = ? OR to_addr = ?"
	args := []any{address, address}
	switch dir {
	case "in":
		where = "WHERE to_addr = ?"
		args = []any{address}
	case "out":
		where = "WHERE from_addr = ?"
		args = []any{address}
	}
	var n int64
	err := c.db.QueryRow("SELECT COUNT(*) FROM transactions "+where, args...).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("chaindb: 統計地址交易失敗: %w", err)
	}
	return n, nil
}

// GetTransactionsByBlock 返回某高度的全部交易（按索引升序）。
func (c *ChainDB) GetTransactionsByBlock(height int64) ([]*Transaction, error) {
	rows, err := c.db.Query("SELECT "+txCols+
		" FROM transactions WHERE block_height = ? ORDER BY tx_index", height)
	if err != nil {
		return nil, fmt.Errorf("chaindb: 按塊查交易失敗: %w", err)
	}
	defer rows.Close()
	return collectTransactions(rows)
}

// GetCoinbaseTransactions 返回鏈上全部 coinbase 交易（memo 恰為 "coinbase" 或前綴 "coinbase:"），
// 供鏈上真實聚合（總產出／礦工收益）——任何節點同步同一條鏈結果一致，
// 不依賴本地 chain_stats/Ledger 快照（M68）。
func (c *ChainDB) GetCoinbaseTransactions() ([]*Transaction, error) {
	rows, err := c.db.Query("SELECT " + txCols +
		" FROM transactions WHERE memo = 'coinbase' OR memo LIKE 'coinbase:%' ORDER BY block_height, tx_index")
	if err != nil {
		return nil, fmt.Errorf("chaindb: 查 coinbase 交易失敗: %w", err)
	}
	defer rows.Close()
	return collectTransactions(rows)
}

func collectTransactions(rows *sql.Rows) ([]*Transaction, error) {
	var out []*Transaction
	for rows.Next() {
		t, err := scanTransaction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetContractTransactions 返回 memo 匹配任一前綴的交易（按高度、索引升序）。
func (c *ChainDB) GetContractTransactions(prefixes ...string) ([]Transaction, error) {
	if len(prefixes) == 0 {
		return nil, nil
	}
	clauses := make([]string, len(prefixes))
	args := make([]any, len(prefixes))
	for i, p := range prefixes {
		clauses[i] = "memo LIKE ?"
		args[i] = p + "%"
	}
	q := "SELECT " + txCols + " FROM transactions WHERE " +
		strings.Join(clauses, " OR ") +
		" ORDER BY block_height ASC, tx_index ASC"
	rows, err := c.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("chaindb: 查合約交易失敗: %w", err)
	}
	defer rows.Close()
	ptr, err := collectTransactions(rows)
	if err != nil {
		return nil, err
	}
	out := make([]Transaction, len(ptr))
	for i := range ptr {
		out[i] = *ptr[i]
	}
	return out, nil
}

// GetAccount 按地址取賬戶；不存在返回 (nil, nil)。
func (c *ChainDB) GetAccount(address string) (*Account, error) {
	if c.cache != nil {
		if a, ok := c.cache.getAccount(address); ok {
			return a, nil
		}
	}
	var (
		a      Account
		pubkey sql.NullString
	)
	err := c.db.QueryRow(
		"SELECT address, balance, nonce, pubkey, first_seen, last_active FROM accounts WHERE address = ?",
		address).Scan(&a.Address, &a.Balance, &a.Nonce, &pubkey, &a.FirstSeen, &a.LastActive)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("chaindb: 查賬戶失敗: %w", err)
	}
	a.Pubkey = pubkey.String
	if c.cache != nil {
		c.cache.putAccount(address, &a)
	}
	return &a, nil
}

// GetBalance 返回地址餘額字符串（無賬戶時為 "0"）。
func (c *ChainDB) GetBalance(address string) string {
	if c.cache != nil {
		if v, ok := c.cache.getBalance(address); ok {
			return v
		}
	}
	var bal string
	err := c.db.QueryRow("SELECT balance FROM accounts WHERE address = ?", address).Scan(&bal)
	if err != nil {
		bal = "0"
	}
	if c.cache != nil {
		c.cache.putBalance(address, bal)
	}
	return bal
}

// GetNonce 返回地址交易序號（防重放）。
func (c *ChainDB) GetNonce(address string) int64 {
	if c.cache != nil {
		if v, ok := c.cache.getNonce(address); ok {
			return v
		}
	}
	var nonce int64
	err := c.db.QueryRow("SELECT nonce FROM accounts WHERE address = ?", address).Scan(&nonce)
	if err != nil {
		nonce = 0
	}
	if c.cache != nil {
		c.cache.putNonce(address, nonce)
	}
	return nonce
}

// ---- 事務內部 helper（供重組重放使用） ----

func getLatestBlockTx(tx *sql.Tx) (*Block, error) {
	row := tx.QueryRow("SELECT " + blockCols + " FROM blocks ORDER BY height DESC LIMIT 1")
	b, err := scanBlock(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return b, err
}

func getBlockTx(tx *sql.Tx, height int64) (*Block, error) {
	row := tx.QueryRow("SELECT "+blockCols+" FROM blocks WHERE height = ?", height)
	b, err := scanBlock(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return b, err
}

func getTransactionsByBlockTx(tx *sql.Tx, height int64) ([]Transaction, error) {
	rows, err := tx.Query("SELECT "+txCols+
		" FROM transactions WHERE block_height = ? ORDER BY tx_index", height)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ptrs, err := collectTransactions(rows)
	if err != nil {
		return nil, err
	}
	out := make([]Transaction, len(ptrs))
	for i := range ptrs {
		out[i] = *ptrs[i]
	}
	return out, nil
}
