package rollup

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const storeSchema = `
CREATE TABLE IF NOT EXISTS l2_accounts (
	address TEXT PRIMARY KEY,
	balance REAL NOT NULL DEFAULT 0,
	nonce INTEGER NOT NULL DEFAULT 0,
	storage_root TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS l2_transactions (
	tx_hash TEXT PRIMARY KEY,
	from_addr TEXT, to_addr TEXT,
	amount REAL, fee REAL, nonce INTEGER, ts INTEGER,
	signature TEXT, pubkey TEXT,
	status TEXT, l2_block INTEGER
);
CREATE TABLE IF NOT EXISTS l2_blocks (
	height INTEGER PRIMARY KEY,
	batch_index INTEGER, tx_count INTEGER,
	tx_hashes TEXT, state_root TEXT, prev_state_root TEXT,
	proposer TEXT, timestamp INTEGER, l1_tx_hash TEXT,
	status TEXT, pre_snapshot TEXT
);
CREATE TABLE IF NOT EXISTS l2_processed_deposits (
	l1_tx_hash TEXT PRIMARY KEY, processed_at INTEGER
);
CREATE TABLE IF NOT EXISTS l2_withdrawals (
	withdraw_id TEXT PRIMARY KEY,
	address TEXT, amount REAL,
	status TEXT, created_at INTEGER, finalized_at INTEGER
);`

// Withdrawal 為 L2→L1 提款記錄。
type Withdrawal struct {
	ID          string  `json:"withdraw_id"`
	Address     string  `json:"address"`
	Amount      float64 `json:"amount"`
	Status      string  `json:"status"`
	CreatedAt   int64   `json:"created_at"`
	FinalizedAt int64   `json:"finalized_at,omitempty"`
}

// Store 為 L2 SQLite 持久化（單連接，事務/查詢串行）。
type Store struct {
	db *sql.DB
}

// OpenStore 打開/創建 L2 庫並初始化表。
func OpenStore(dbPath string) (*Store, error) {
	if dir := filepath.Dir(dbPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("rollup: 創建 L2 目錄失敗: %w", err)
		}
	}
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(30000)")
	if err != nil {
		return nil, fmt.Errorf("rollup: 打開 L2 庫失敗: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(storeSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("rollup: 初始化 L2 表失敗: %w", err)
	}
	return &Store{db: db}, nil
}

// Close 關閉數據庫。
func (s *Store) Close() error { return s.db.Close() }

// SaveAccount upsert 一個 L2 賬戶。
func (s *Store) SaveAccount(a L2Account) error {
	if a.StorageRoot == "" {
		a.StorageRoot = ZeroRoot
	}
	_, err := s.db.Exec(`INSERT OR REPLACE INTO l2_accounts
		(address,balance,nonce,storage_root) VALUES(?,?,?,?)`,
		a.Address, a.Balance, a.Nonce, a.StorageRoot)
	if err != nil {
		return fmt.Errorf("rollup: 保存賬戶失敗: %w", err)
	}
	return nil
}

// SaveTx upsert 一筆 L2 交易。
func (s *Store) SaveTx(tx L2Transaction) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO l2_transactions
		(tx_hash,from_addr,to_addr,amount,fee,nonce,ts,signature,pubkey,status,l2_block)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		tx.TxHash, tx.FromAddr, tx.ToAddr, tx.Amount, tx.Fee, tx.Nonce, tx.Ts,
		tx.Signature, tx.Pubkey, tx.Status, tx.L2Block)
	if err != nil {
		return fmt.Errorf("rollup: 保存交易失敗: %w", err)
	}
	return nil
}

// SaveBlock upsert 一個 L2 塊（序列化 tx_hashes 與 pre_snapshot）。
func (s *Store) SaveBlock(b L2Block) error {
	txHashes, err := json.Marshal(b.TxHashes)
	if err != nil {
		return fmt.Errorf("rollup: 序列化 tx_hashes 失敗: %w", err)
	}
	var preSnap any
	if b.PreSnapshot != nil {
		raw, err := json.Marshal(b.PreSnapshot)
		if err != nil {
			return fmt.Errorf("rollup: 序列化快照失敗: %w", err)
		}
		preSnap = string(raw)
	}
	_, err = s.db.Exec(`INSERT OR REPLACE INTO l2_blocks
		(height,batch_index,tx_count,tx_hashes,state_root,prev_state_root,
			proposer,timestamp,l1_tx_hash,status,pre_snapshot)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		b.Height, b.BatchIndex, b.TxCount, string(txHashes),
		b.StateRoot, b.PrevStateRoot, b.Proposer, b.Timestamp,
		nullableStr(b.L1TxHash), b.Status, preSnap)
	if err != nil {
		return fmt.Errorf("rollup: 保存塊失敗: %w", err)
	}
	return nil
}

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// LoadAccounts 讀取全部 L2 賬戶。
func (s *Store) LoadAccounts() ([]L2Account, error) {
	rows, err := s.db.Query(
		"SELECT address,balance,nonce,storage_root FROM l2_accounts ORDER BY address")
	if err != nil {
		return nil, fmt.Errorf("rollup: 讀賬戶失敗: %w", err)
	}
	defer rows.Close()
	var out []L2Account
	for rows.Next() {
		var a L2Account
		if err := rows.Scan(&a.Address, &a.Balance, &a.Nonce, &a.StorageRoot); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// LoadBlocks 讀取全部 L2 塊（按高度升序，反序列化）。
func (s *Store) LoadBlocks() ([]L2Block, error) {
	rows, err := s.db.Query(`SELECT height,batch_index,tx_count,tx_hashes,
		state_root,prev_state_root,proposer,timestamp,l1_tx_hash,status,pre_snapshot
		FROM l2_blocks ORDER BY height`)
	if err != nil {
		return nil, fmt.Errorf("rollup: 讀塊失敗: %w", err)
	}
	defer rows.Close()

	var out []L2Block
	for rows.Next() {
		var (
			b                             L2Block
			txHashes, preSnap, l1Tx       sql.NullString
			proposer, stateRoot, prevRoot sql.NullString
		)
		if err := rows.Scan(&b.Height, &b.BatchIndex, &b.TxCount, &txHashes,
			&stateRoot, &prevRoot, &proposer, &b.Timestamp, &l1Tx, &b.Status,
			&preSnap); err != nil {
			return nil, err
		}
		b.StateRoot = stateRoot.String
		b.PrevStateRoot = prevRoot.String
		b.Proposer = proposer.String
		b.L1TxHash = l1Tx.String
		if txHashes.Valid && txHashes.String != "" {
			if err := json.Unmarshal([]byte(txHashes.String), &b.TxHashes); err != nil {
				return nil, fmt.Errorf("rollup: tx_hashes JSON 錯誤: %w", err)
			}
		}
		if preSnap.Valid && preSnap.String != "" {
			var ts TreeSnapshot
			if err := json.Unmarshal([]byte(preSnap.String), &ts); err != nil {
				return nil, fmt.Errorf("rollup: 快照 JSON 錯誤: %w", err)
			}
			b.PreSnapshot = &ts
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// LoadPendingTxs 讀取仍待打包的交易。
func (s *Store) LoadPendingTxs() ([]L2Transaction, error) {
	rows, err := s.db.Query(`SELECT tx_hash,from_addr,to_addr,amount,fee,nonce,ts,
		signature,pubkey,status,l2_block FROM l2_transactions WHERE status=?`,
		StatusPending)
	if err != nil {
		return nil, fmt.Errorf("rollup: 讀待處理交易失敗: %w", err)
	}
	defer rows.Close()
	var out []L2Transaction
	for rows.Next() {
		var tx L2Transaction
		if err := scanTx(rows, &tx); err != nil {
			return nil, err
		}
		out = append(out, tx)
	}
	return out, rows.Err()
}

// GetTx 按哈希取交易；不存在返回 (nil,nil)。
func (s *Store) GetTx(hash string) (*L2Transaction, error) {
	row := s.db.QueryRow(`SELECT tx_hash,from_addr,to_addr,amount,fee,nonce,ts,
		signature,pubkey,status,l2_block FROM l2_transactions WHERE tx_hash=?`, hash)
	var tx L2Transaction
	if err := scanTx(row, &tx); err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("rollup: 查交易失敗: %w", err)
	}
	return &tx, nil
}

// GetTxsByHashes 按哈希順序批量取交易（欺詐重放用）。
func (s *Store) GetTxsByHashes(hashes []string) ([]L2Transaction, error) {
	out := make([]L2Transaction, 0, len(hashes))
	for _, h := range hashes {
		tx, err := s.GetTx(h)
		if err != nil {
			return nil, err
		}
		if tx != nil {
			out = append(out, *tx)
		}
	}
	return out, nil
}

type rowScanner interface{ Scan(dest ...any) error }

func scanTx(sc rowScanner, tx *L2Transaction) error {
	return sc.Scan(&tx.TxHash, &tx.FromAddr, &tx.ToAddr, &tx.Amount, &tx.Fee,
		&tx.Nonce, &tx.Ts, &tx.Signature, &tx.Pubkey, &tx.Status, &tx.L2Block)
}

// IsDepositProcessed 判斷 L1 存款是否已入賬（防重放）。
func (s *Store) IsDepositProcessed(l1TxHash string) (bool, error) {
	var n int
	err := s.db.QueryRow(
		"SELECT COUNT(1) FROM l2_processed_deposits WHERE l1_tx_hash=?",
		l1TxHash).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// MarkDepositProcessed 標記 L1 存款已處理。
func (s *Store) MarkDepositProcessed(l1TxHash string, ts int64) error {
	_, err := s.db.Exec(
		"INSERT OR REPLACE INTO l2_processed_deposits VALUES(?,?)",
		l1TxHash, ts)
	if err != nil {
		return fmt.Errorf("rollup: 標記存款失敗: %w", err)
	}
	return nil
}

// SaveWithdrawal upsert 提款記錄。
func (s *Store) SaveWithdrawal(w Withdrawal) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO l2_withdrawals
		(withdraw_id,address,amount,status,created_at,finalized_at)
		VALUES(?,?,?,?,?,?)`,
		w.ID, w.Address, w.Amount, w.Status, w.CreatedAt,
		nullableInt64(w.FinalizedAt))
	if err != nil {
		return fmt.Errorf("rollup: 保存提款失敗: %w", err)
	}
	return nil
}

func nullableInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// GetWithdrawal 取提款記錄；不存在返回 (nil,nil)。
func (s *Store) GetWithdrawal(id string) (*Withdrawal, error) {
	row := s.db.QueryRow(`SELECT withdraw_id,address,amount,status,
		created_at,finalized_at FROM l2_withdrawals WHERE withdraw_id=?`, id)
	var (
		w     Withdrawal
		final sql.NullInt64
	)
	err := row.Scan(&w.ID, &w.Address, &w.Amount, &w.Status, &w.CreatedAt, &final)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("rollup: 查提款失敗: %w", err)
	}
	w.FinalizedAt = final.Int64
	return &w, nil
}
