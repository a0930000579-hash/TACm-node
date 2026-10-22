package chaindb

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// 狀態膨脹治理（v156 A1）：對「已最終化且超過保留期」的歷史區塊 / 交易明細 / 事件日誌
// 做可配置的清理或歸檔。
//
// 安全模型（極重要，整合者必須理解）：
//   - 本鏈的當前餘額是「物質化帳本」：accounts.balance / accounts.nonce 在 InsertBlock
//     時已完成轉換寫入，**不是**從 transactions 即時重算。因此刪除舊 blocks/transactions/
//     logs 列完全不改變 accounts 表，GetBalance / GetNonce / GetAccount 結果保持正確。
//   - 本函式 **絕不碰** live-state 表：accounts、mempool、pos_bootstraps、node_registry、
//     pos_validators、delegations、unbondings、peers。這些表是當前共識 / 質押 / 錢包狀態。
//   - 只刪 height < cutoff 的歷史列，且 cutoff 永遠距 tip 至少 SafetyBlocks（重組緩衝）。
//   - 副作用：prune 後 RebuildAccounts 只能從最舊保留塊開始重放（無法重建被刪除的早期塊）。
//     這是 pruned/archive node 的固有取捨；要完整歸檔請設定 ArchivePath。

// PruneConfig 控制一次清理 / 歸檔的行為。預設值必須保守（見 DefaultPruneConfig）。
type PruneConfig struct {
	// RetentionBlocks 保留區塊數：只刪 height < tip-RetentionBlocks 的歷史列。
	// 設為 0 代表「永不自動刪除」（預設安全值由 stateprune 層給予極大值）。
	RetentionBlocks int64
	// SafetyBlocks 重組安全邊界：cutoff 永遠 <= tip-SafetyBlocks，避免刪到可能被重組的最近塊。
	SafetyBlocks int64
	// RetentionAgeSec 時間保留閘：若 cutoff 處區塊的時間戳比 now-RetentionAgeSec 還新，則本次略過。
	// 0 代表不檢查時間。用於「高度保留夠但鏈還很年輕時不刪」。
	RetentionAgeSec int64
	// ArchivePath 若非空，刪除前把歷史列複製到這個獨立 SQLite 歸檔檔（archive.db）。
	ArchivePath string
	// Vacuum 刪除後是否執行 VACUUM 回收檔案空間（較重；大庫建議在低峰執行）。
	Vacuum bool
	// BatchSize 每次刪除的高度區間大小（避免單一事務鎖太久）。
	BatchSize int
}

// DefaultPruneConfig 給出極保守的預設：保留 50 萬塊、100 塊重組緩衝、30 天時間閘、
// 不自動 VACUUM。主網預設不會實際刪除任何近期資料。
func DefaultPruneConfig() PruneConfig {
	return PruneConfig{
		RetentionBlocks: 500_000,
		SafetyBlocks:    100,
		RetentionAgeSec: 30 * 24 * 3600,
		ArchivePath:     "",
		Vacuum:          false,
		BatchSize:       1000,
	}
}

// PruneStats 記錄一次清理的結果（DB 位元組變化與刪除列數）。
type PruneStats struct {
	DBBytesBefore int64 `json:"db_bytes_before"`
	DBBytesAfter  int64 `json:"db_bytes_after"`
	PrunedBlocks  int64 `json:"pruned_blocks"`
	PrunedTxs     int64 `json:"pruned_txs"`
	PrunedLogs    int64 `json:"pruned_logs"`
	CutoffHeight  int64 `json:"cutoff_height"` // 被刪除的高度上限（height < cutoff）
	OldestKept    int64 `json:"oldest_kept"`   // 清理後最舊保留塊高度（空庫為 0）
	Retention     int64 `json:"retention_blocks"`
	Archived      bool  `json:"archived"`
	RunAt         int64 `json:"run_at_unix"`
}

// dbFileBytes 讀取主資料庫檔案（不含 -wal/-shm）的位元組數。
func (c *ChainDB) dbFileBytes() int64 {
	fi, err := os.Stat(c.path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// walCheckpointTruncate 把 WAL 併回主檔並截斷，使 dbFileBytes 反映真實占用。
func (c *ChainDB) walCheckpointTruncate() {
	// 錯誤忽略（忙碌時下次再試），不影響清理本身。
	_, _ = c.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
}

// PruneArchive 執行一次「歷史清理 + 可選歸檔」。可安全重複呼叫（冪等：沒有可刪列時回零）。
//
// cutoff 決定：cutoff = tip - RetentionBlocks，且 cutoff <= tip - SafetyBlocks。
// 若 RetentionBlocks <= 0 或 cutoff <= 0，直接回零（不刪任何東西）。
func (c *ChainDB) PruneArchive(cfg PruneConfig) (PruneStats, error) {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 1000
	}
	now := time.Now().Unix()
	st := PruneStats{Retention: cfg.RetentionBlocks, RunAt: now}

	tip := c.GetTipHeight()
	if tip <= 0 {
		return st, nil
	}
	// 保留期未開或小於安全邊界 → 不刪。
	if cfg.RetentionBlocks <= 0 {
		return st, nil
	}
	cutoff := tip - cfg.RetentionBlocks
	if cutoff <= 0 {
		return st, nil
	}
	if cutoff > tip-cfg.SafetyBlocks {
		cutoff = tip - cfg.SafetyBlocks
	}
	if cutoff <= 0 {
		return st, nil
	}

	// 時間閘：cutoff 處（即將被刪的最年輕塊）的時間戳若不夠老，代表鏈整體仍年輕，略過。
	if cfg.RetentionAgeSec > 0 {
		var cutoffTs sql.NullInt64
		// 找最接近 cutoff 的保留臨界塊：高度 < cutoff 中最大者。
		err := c.db.QueryRow("SELECT ts FROM blocks WHERE height < ? ORDER BY height DESC LIMIT 1", cutoff).
			Scan(&cutoffTs)
		if err == nil && cutoffTs.Valid {
			if now-cutoffTs.Int64 < cfg.RetentionAgeSec {
				return st, nil
			}
		}
	}

	// 最舊塊。
	var oldest sql.NullInt64
	if err := c.db.QueryRow("SELECT MIN(height) FROM blocks").Scan(&oldest); err != nil {
		return st, fmt.Errorf("chaindb: 查最舊塊失敗: %w", err)
	}
	if !oldest.Valid || oldest.Int64 >= cutoff {
		// 沒有任何可刪除的歷史列。
		return st, nil
	}

	c.walCheckpointTruncate()
	st.DBBytesBefore = c.dbFileBytes()

	// 可選：先歸檔。
	var arch *sql.DB
	if cfg.ArchivePath != "" {
		a, err := openArchiveDB(cfg.ArchivePath)
		if err != nil {
			return st, fmt.Errorf("chaindb: 開啟歸檔庫失敗: %w", err)
		}
		defer func() { _ = a.Close() }()
		arch = a
	}

	// 事務化：先歸檔歷史列，再分批刪 transactions/logs，最後刪 blocks。
	// 整體放一個事務，失敗回滾保證不半刪。
	tx, err := c.db.Begin()
	if err != nil {
		return st, fmt.Errorf("chaindb: prune 開事務失敗: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	lo := oldest.Int64
	// 分批處理 [lo, cutoff) 這個高度區間。
	for h := lo; h < cutoff; h += int64(cfg.BatchSize) {
		hi := h + int64(cfg.BatchSize)
		if hi > cutoff {
			hi = cutoff
		}
		if arch != nil {
			if err := copyRangeToArchive(tx, arch, h, hi); err != nil {
				return st, fmt.Errorf("chaindb: 歸檔區間 [%d,%d) 失敗: %w", h, hi, err)
			}
		}
		resT, err := tx.Exec("DELETE FROM transactions WHERE block_height >= ? AND block_height < ?", h, hi)
		if err != nil {
			return st, fmt.Errorf("chaindb: 刪交易 [%d,%d) 失敗: %w", h, hi, err)
		}
		nT, _ := resT.RowsAffected()
		st.PrunedTxs += nT
		resL, err := tx.Exec("DELETE FROM logs WHERE block_height >= ? AND block_height < ?", h, hi)
		if err != nil {
			return st, fmt.Errorf("chaindb: 刪日誌 [%d,%d) 失敗: %w", h, hi, err)
		}
		nL, _ := resL.RowsAffected()
		st.PrunedLogs += nL
	}
	// 刪 blocks（此時已無子交易 / 日誌引用）。
	resB, err := tx.Exec("DELETE FROM blocks WHERE height < ?", cutoff)
	if err != nil {
		return st, fmt.Errorf("chaindb: 刪 blocks (<%d) 失敗: %w", cutoff, err)
	}
	st.PrunedBlocks, _ = resB.RowsAffected()

	if err := tx.Commit(); err != nil {
		return st, fmt.Errorf("chaindb: prune 提交失敗: %w", err)
	}
	st.CutoffHeight = cutoff
	st.Archived = arch != nil

	// 清理讀快取（保守：被刪的舊交易可能曾被快取）。
	if c.cache != nil {
		c.cache.clear()
	}

	if cfg.Vacuum {
		// VACUUM 失敗不視為致命（資料已刪，只是檔案未回收）。
		_, _ = c.db.Exec("VACUUM")
	}
	c.walCheckpointTruncate()
	st.DBBytesAfter = c.dbFileBytes()

	// 最舊保留塊。
	var ok sql.NullInt64
	if err := c.db.QueryRow("SELECT MIN(height) FROM blocks").Scan(&ok); err == nil && ok.Valid {
		st.OldestKept = ok.Int64
	}
	return st, nil
}

// openArchiveDB 建立（或開啟）獨立歸檔 SQLite 庫，表結構鏡像 blocks/transactions/logs。
func openArchiveDB(path string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	dsn := path + "?_pragma=busy_timeout(30000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	schema := `
	CREATE TABLE IF NOT EXISTS archive_blocks (
		height INTEGER PRIMARY KEY, hash TEXT, prev_hash TEXT, merkle_root TEXT,
		proposer TEXT, proposer_address TEXT, ts INTEGER, tx_count INTEGER,
		difficulty INTEGER, nonce INTEGER, size INTEGER, base_fee TEXT,
		gas_used INTEGER, gas_limit INTEGER, burned TEXT
	);
	CREATE TABLE IF NOT EXISTS archive_txs (
		tx_hash TEXT PRIMARY KEY, block_height INTEGER, block_hash TEXT, tx_index INTEGER,
		from_addr TEXT, to_addr TEXT, amount TEXT, fee TEXT, gas_limit INTEGER,
		gas_used INTEGER, max_fee TEXT, priority_fee TEXT, burned TEXT, nonce INTEGER,
		ts INTEGER, signature TEXT, pubkey TEXT, memo TEXT, status TEXT
	);
	CREATE TABLE IF NOT EXISTS archive_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT, tx_hash TEXT, block_height INTEGER,
		tx_index INTEGER, log_index INTEGER, address TEXT, topic0 TEXT, topic1 TEXT,
		topic2 TEXT, topic3 TEXT, data TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_arch_tx_block ON archive_txs(block_height);
	CREATE INDEX IF NOT EXISTS idx_arch_log_block ON archive_logs(block_height);
	`
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// copyRangeToArchive 把主庫 [lo,hi) 高度的 blocks/txs/logs 複製到歸檔庫。
// 失敗時整個 prune 事務回滾（呼叫端處理）。
func copyRangeToArchive(src *sql.Tx, arch *sql.DB, lo, hi int64) error {
	bRows, err := src.Query("SELECT "+blockCols+" FROM blocks WHERE height >= ? AND height < ?", lo, hi)
	if err != nil {
		return err
	}
	defer bRows.Close()
	aTx, err := arch.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = aTx.Rollback() }()
	for bRows.Next() {
		b, err := scanBlock(bRows)
		if err != nil {
			return err
		}
		var prev any
		if b.PrevHash != nil {
			prev = *b.PrevHash
		}
		if _, err := aTx.Exec(`INSERT OR IGNORE INTO archive_blocks
			(height,hash,prev_hash,merkle_root,proposer,proposer_address,ts,tx_count,difficulty,nonce,size,base_fee,gas_used,gas_limit,burned)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			b.Height, b.Hash, prev, b.MerkleRoot, b.Proposer, b.ProposerAddress, b.Ts,
			b.TxCount, b.Difficulty, b.Nonce, b.Size, b.BaseFee, b.GasUsed, b.GasLimit, b.Burned); err != nil {
			return err
		}
	}
	tRows, err := src.Query("SELECT "+txCols+" FROM transactions WHERE block_height >= ? AND block_height < ?", lo, hi)
	if err != nil {
		return err
	}
	defer tRows.Close()
	for tRows.Next() {
		t, err := scanTransaction(tRows)
		if err != nil {
			return err
		}
		if _, err := aTx.Exec(`INSERT OR IGNORE INTO archive_txs
			(tx_hash,block_height,block_hash,tx_index,from_addr,to_addr,amount,fee,gas_limit,gas_used,max_fee,priority_fee,burned,nonce,ts,signature,pubkey,memo,status)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			t.TxHash, t.BlockHeight, t.BlockHash, t.TxIndex, t.FromAddr, t.ToAddr, t.Amount,
			t.Fee, t.GasLimit, t.GasUsed, t.MaxFee, t.PriorityFee, t.Burned, t.Nonce, t.Ts,
			t.Signature, t.Pubkey, t.Memo, t.Status); err != nil {
			return err
		}
	}
	lRows, err := src.Query(`SELECT id,tx_hash,block_height,tx_index,log_index,address,topic0,topic1,topic2,topic3,data
		FROM logs WHERE block_height >= ? AND block_height < ?`, lo, hi)
	if err != nil {
		return err
	}
	defer lRows.Close()
	for lRows.Next() {
		var (
			id, bh, li         int64
			txh, addr          string
			t0, t1, t2, t3, dt sql.NullString
			ti                 int
		)
		if err := lRows.Scan(&id, &txh, &bh, &ti, &li, &addr, &t0, &t1, &t2, &t3, &dt); err != nil {
			return err
		}
		if _, err := aTx.Exec(`INSERT OR IGNORE INTO archive_logs
			(id,tx_hash,block_height,tx_index,log_index,address,topic0,topic1,topic2,topic3,data)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			id, txh, bh, ti, li, addr, t0.String, t1.String, t2.String, t3.String, dt.String); err != nil {
			return err
		}
	}
	return aTx.Commit()
}
