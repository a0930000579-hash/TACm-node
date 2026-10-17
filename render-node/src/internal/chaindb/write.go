package chaindb

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// InsertBlock 在單一事務內插入區塊及其交易，順序完成：
// 寫塊 → 逐筆寫交易並更新賬戶（手續費結給提議者）→ 清理內存池 → coinbase 增發。
// 任何一步失敗均回滾並返回錯誤。
func (c *ChainDB) InsertBlock(block *Block, txs []Transaction) error {
	tx, err := c.db.Begin()
	if err != nil {
		return fmt.Errorf("chaindb: 開啟事務失敗: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// 區塊序列化大小（對照 Python len(json.dumps(block))）。
	rawSize, err := json.Marshal(block)
	if err != nil {
		return fmt.Errorf("chaindb: 計算區塊大小失敗: %w", err)
	}
	size := len(rawSize)
	if _, err := tx.Exec(`
		INSERT OR REPLACE INTO blocks
		(height, hash, prev_hash, merkle_root, proposer, proposer_address, ts, tx_count, difficulty, nonce, size,
		 base_fee, gas_used, gas_limit, burned)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		block.Height, block.Hash, block.PrevHash, block.MerkleRoot,
		block.Proposer, block.ProposerAddress, block.Ts,
		txCount(block, txs), block.Difficulty, block.Nonce, size,
		strOr(block.BaseFee), block.GasUsed, block.GasLimit, strOr(block.Burned)); err != nil {
		return fmt.Errorf("chaindb: 寫入區塊 %d 失敗: %w", block.Height, err)
	}

	proposer := block.ProposerAddress
	if proposer == "" {
		proposer = block.Proposer
	}

	for i := range txs {
		t := &txs[i]
		if _, err := tx.Exec(`
			INSERT OR REPLACE INTO transactions
			(tx_hash, block_height, block_hash, tx_index, from_addr, to_addr,
			 amount, fee, gas_limit, gas_used, max_fee, priority_fee, burned, nonce, ts, signature, pubkey, memo, status)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'confirmed')`,
			t.TxHash, block.Height, block.Hash, i, t.FromAddr, t.ToAddr,
			strOr(t.Amount), strOr(t.Fee), t.GasLimit, t.GasUsed, strOr(t.MaxFee), strOr(t.PriorityFee),
			strOr(t.Burned), t.Nonce, t.Ts, t.Signature, t.Pubkey, t.Memo); err != nil {
			return fmt.Errorf("chaindb: 寫入交易 %s 失敗: %w", t.TxHash, err)
		}
		// M74-3 EIP-1559：burn 部分從付款人扣除但不入任何帳戶（銷毀通縮）；
		// proposer 僅得 tip（fee−burn）。legacy 交易 burn=0 行為與舊版一致。
		if err := updateBalance(tx, t.FromAddr, t.ToAddr,
			strOr(t.Amount), strOr(t.Fee), strOr(t.Burned), proposer); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM mempool WHERE tx_hash = ?", t.TxHash); err != nil {
			return fmt.Errorf("chaindb: 清理內存池失敗: %w", err)
		}
	}

	// coinbase 增發已作為 txs 中的 coinbase 交易（from 為空）由 updateBalance 入賬，
	// 此處不再單獨 credit，避免雙發。

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("chaindb: 提交區塊 %d 失敗: %w", block.Height, err)
	}
	if c.cache != nil {
		c.cache.clear()
	}
	return nil
}

func txCount(block *Block, txs []Transaction) int {
	if block.TxCount != 0 {
		return block.TxCount
	}
	return len(txs)
}

func strOr(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

// applyDelta 對賬戶餘額施加增量，保留 nonce/pubkey；incNonce 時 nonce+1。
// 空地址直接忽略。
func applyDelta(tx *sql.Tx, address string, delta float64, incNonce bool) error {
	if address == "" {
		return nil
	}
	var (
		balStr, pubkey sql.NullString
		nonce          sql.NullInt64
		firstSeen      sql.NullInt64
	)
	err := tx.QueryRow(
		"SELECT balance, nonce, pubkey, first_seen FROM accounts WHERE address = ?",
		address).Scan(&balStr, &nonce, &pubkey, &firstSeen)

	var (
		bal, non float64
		pub      any
		first    int64
	)
	switch err {
	case nil:
		bal = parseFloat(balStr.String)
		non = float64(nonce.Int64)
		if pubkey.Valid {
			pub = pubkey.String
		}
		if firstSeen.Valid && firstSeen.Int64 != 0 {
			first = firstSeen.Int64
		} else {
			first = nowUnix()
		}
	case sql.ErrNoRows:
		bal, non, pub = 0, 0, nil
		first = nowUnix()
	default:
		return fmt.Errorf("chaindb: 讀取賬戶 %s 失敗: %w", address, err)
	}

	if incNonce {
		non++
	}
	if _, err := tx.Exec(`
		INSERT OR REPLACE INTO accounts
		(address, balance, nonce, pubkey, first_seen, last_active)
		VALUES (?, ?, ?, ?, ?, ?)`,
		address, FormatFloat(bal+delta), int64(non), pub, first, nowUnix()); err != nil {
		return fmt.Errorf("chaindb: 更新賬戶 %s 失敗: %w", address, err)
	}
	return nil
}

// updateBalance 執行賬戶狀態轉換：發方扣款（含費、nonce+1）、收方入賬、
// 手續費結給提議者（提議者即付款方時不重複計）。
func updateBalance(tx *sql.Tx, fromAddr, toAddr, amount, fee, burned, proposer string) error {
	amt := parseFloat(amount)
	feeAmt := parseFloat(fee)
	burnAmt := parseFloat(burned)
	if fromAddr != "" {
		if err := applyDelta(tx, fromAddr, -(amt + feeAmt), true); err != nil {
			return err
		}
	}
	if toAddr != "" {
		if err := applyDelta(tx, toAddr, amt, false); err != nil {
			return err
		}
	}
	// M74-3：出塊者實得 tip = fee − burn（burn 銷毀、不入任何帳戶）。
	tip := feeAmt - burnAmt
	if tip > 0 && proposer != "" && proposer != fromAddr {
		if err := applyDelta(tx, proposer, tip, false); err != nil {
			return err
		}
	}
	return nil
}

// credit 為純增發（coinbase）：給地址增加餘額，保留 nonce/pubkey。
func credit(tx *sql.Tx, address string, amount float64) error {
	if address == "" || amount <= 0 {
		return nil
	}
	return applyDelta(tx, address, amount, false)
}

// ---- 鏈重組 ----

// TruncateFromHeight 刪除 height 及之後的區塊/交易並清空內存池（分叉切換前使用）。
func (c *ChainDB) TruncateFromHeight(height int64) error {
	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec("DELETE FROM transactions WHERE block_height >= ?", height); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM blocks WHERE height >= ?", height); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM mempool"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if c.cache != nil {
		c.cache.clear()
	}
	return nil
}

// RebuildAccounts 從創世起順序重放全部區塊，重建賬戶餘額，返回鏈頂高度。
func (c *ChainDB) RebuildAccounts() (int64, error) {
	tx, err := c.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec("DELETE FROM accounts"); err != nil {
		return 0, err
	}
	latest, err := getLatestBlockTx(tx)
	if err != nil {
		return 0, err
	}
	var tip int64
	if latest != nil {
		tip = latest.Height
	}
	for h := int64(0); h <= tip; h++ {
		block, err := getBlockTx(tx, h)
		if err != nil {
			return 0, err
		}
		if block == nil {
			continue
		}
		proposer := block.ProposerAddress
		if proposer == "" {
			proposer = block.Proposer
		}
		txs, err := getTransactionsByBlockTx(tx, h)
		if err != nil {
			return 0, err
		}
		for i := range txs {
			t := &txs[i]
			if err := updateBalance(tx, t.FromAddr, t.ToAddr, t.Amount, t.Fee, t.Burned, proposer); err != nil {
				return 0, err
			}
		}
		// coinbase 交易已在 txs 中（from 為空、to 為提議者），無需再單獨增發。
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if c.cache != nil {
		c.cache.clear()
	}
	return tip, nil
}

// CumulativeDifficulty 返回 (startH, endH] 區間內難度之和。
func (c *ChainDB) CumulativeDifficulty(startH, endH int64) (int64, error) {
	var sum int64
	err := c.db.QueryRow(
		"SELECT COALESCE(SUM(difficulty),0) FROM blocks WHERE height > ? AND height <= ?",
		startH, endH).Scan(&sum)
	if err != nil {
		return 0, err
	}
	return sum, nil
}

// SetAccountPubkey 記錄地址對應公鑰，保留已有餘額與首次出現時間。
func (c *ChainDB) SetAccountPubkey(address, pubkey string) error {
	now := nowUnix()
	_, err := c.db.Exec(`
		INSERT OR REPLACE INTO accounts (address, balance, pubkey, first_seen, last_active)
		VALUES (?, COALESCE((SELECT balance FROM accounts WHERE address = ?), '0'), ?,
		        COALESCE((SELECT first_seen FROM accounts WHERE address = ?), ?), ?)`,
		address, address, pubkey, address, now, now)
	if err != nil {
		return fmt.Errorf("chaindb: 設置公鑰失敗: %w", err)
	}
	if c.cache != nil {
		c.cache.clear()
	}
	return nil
}
