package chaindb

import "fmt"

// GetBlockTxs 返回指定高度的全部交易（按 tx_index 排序）。
func (c *ChainDB) GetBlockTxs(height int64) ([]Transaction, error) {
	rows, err := c.db.Query(`SELECT tx_hash, block_height, block_hash, tx_index,
		from_addr, to_addr, amount, fee, gas_limit, gas_used, max_fee, priority_fee,
		burned, nonce, ts, signature, pubkey, memo, status
		FROM transactions WHERE block_height = ? ORDER BY tx_index`, height)
	if err != nil {
		return nil, fmt.Errorf("chaindb: 查交易失敗: %w", err)
	}
	defer rows.Close()
	var out []Transaction
	for rows.Next() {
		var t Transaction
		if err := rows.Scan(&t.TxHash, &t.BlockHeight, &t.BlockHash, &t.TxIndex,
			&t.FromAddr, &t.ToAddr, &t.Amount, &t.Fee, &t.GasLimit, &t.GasUsed,
			&t.MaxFee, &t.PriorityFee, &t.Burned, &t.Nonce, &t.Ts, &t.Signature,
			&t.Pubkey, &t.Memo, &t.Status); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ImportAccountState 覆寫 chaindb accounts 表的地址餘額與 nonce（快照導入用）。
func (c *ChainDB) ImportAccountState(address, balance string, nonce int64) error {
	_, err := c.db.Exec(`INSERT OR REPLACE INTO accounts (address, balance, nonce) VALUES (?, ?, ?)`,
		address, balance, nonce)
	if err != nil {
		return fmt.Errorf("chaindb: 導入帳戶狀態失敗: %w", err)
	}
	if c.cache != nil {
		c.cache.clear()
	}
	return nil
}
