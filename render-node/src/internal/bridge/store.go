package bridge

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS bridge_transactions (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	bridge_tx_id TEXT UNIQUE,
	source_chain TEXT, target_chain TEXT,
	source_address TEXT, target_address TEXT,
	amount REAL, fee REAL, received_amount REAL,
	token TEXT, status TEXT,
	source_tx_hash TEXT, target_tx_hash TEXT,
	lock_tx_hash TEXT, unlock_tx_hash TEXT,
	nonce INTEGER,
	created_at INTEGER, updated_at INTEGER, confirmed_at INTEGER,
	error TEXT, signatures TEXT
);
CREATE TABLE IF NOT EXISTS bridge_validators (
	address TEXT PRIMARY KEY,
	name TEXT, public_key TEXT,
	is_active INTEGER DEFAULT 1,
	joined_at INTEGER
);
CREATE TABLE IF NOT EXISTS bridge_config (
	key TEXT PRIMARY KEY, value TEXT
);
CREATE INDEX IF NOT EXISTS idx_bridge_status ON bridge_transactions(status);
CREATE INDEX IF NOT EXISTS idx_bridge_addr
	ON bridge_transactions(source_address, target_address);`

// Store 為跨鏈橋 SQLite（單連接串行）。
type Store struct{ db *sql.DB }

// OpenStore 打開/創建橋庫並初始化。
func OpenStore(dbPath string) (*Store, error) {
	if dir := filepath.Dir(dbPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("bridge: 創建目錄失敗: %w", err)
		}
	}
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(30000)")
	if err != nil {
		return nil, fmt.Errorf("bridge: 打開庫失敗: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("bridge: 初始化表失敗: %w", err)
	}
	return &Store{db: db}, nil
}

// Close 關閉庫。
func (s *Store) Close() error { return s.db.Close() }

// CreateBridgeTx 插入跨鏈交易並回填自增 ID。
func (s *Store) CreateBridgeTx(tx *BridgeTx) error {
	sigs, _ := json.Marshal(tx.Signatures)
	res, err := s.db.Exec(`INSERT INTO bridge_transactions
		(bridge_tx_id,source_chain,target_chain,source_address,target_address,
			amount,fee,received_amount,token,status,nonce,
			created_at,updated_at,signatures)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		tx.BridgeTxID, tx.SourceChain, tx.TargetChain,
		tx.SourceAddress, tx.TargetAddress,
		tx.Amount, tx.Fee, tx.ReceivedAmount, tx.Token, tx.Status, tx.Nonce,
		tx.CreatedAt, tx.UpdatedAt, string(sigs))
	if err != nil {
		return fmt.Errorf("bridge: 插入交易失敗: %w", err)
	}
	tx.ID, _ = res.LastInsertId()
	return nil
}

// UpdateBridgeTx 持久化交易最新狀態/哈希/簽名。
func (s *Store) UpdateBridgeTx(tx *BridgeTx) error {
	sigs, _ := json.Marshal(tx.Signatures)
	_, err := s.db.Exec(`UPDATE bridge_transactions SET
		status=?,source_tx_hash=?,target_tx_hash=?,lock_tx_hash=?,
		unlock_tx_hash=?,updated_at=?,confirmed_at=?,error=?,signatures=?
		WHERE bridge_tx_id=?`,
		tx.Status, nullable(tx.SourceTxHash), nullable(tx.TargetTxHash),
		nullable(tx.LockTxHash), nullable(tx.UnlockTxHash),
		tx.UpdatedAt, nullableInt64(tx.ConfirmedAt), nullable(tx.Error),
		string(sigs), tx.BridgeTxID)
	if err != nil {
		return fmt.Errorf("bridge: 更新交易失敗: %w", err)
	}
	return nil
}

func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func nullableInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// GetBridgeTx 按 bridge_tx_id 查詢；不存在返回 (nil,nil)。
func (s *Store) GetBridgeTx(id string) (*BridgeTx, error) {
	row := s.db.QueryRow(`SELECT id,bridge_tx_id,source_chain,target_chain,
		source_address,target_address,amount,fee,received_amount,token,status,
		source_tx_hash,target_tx_hash,lock_tx_hash,unlock_tx_hash,nonce,
		created_at,updated_at,confirmed_at,error,signatures
		FROM bridge_transactions WHERE bridge_tx_id=?`, id)
	tx := &BridgeTx{}
	var (
		sourceTx, targetTx, lockTx, unlockTx, errStr, sigs sql.NullString
		confirmed                                          sql.NullInt64
	)
	err := row.Scan(&tx.ID, &tx.BridgeTxID, &tx.SourceChain, &tx.TargetChain,
		&tx.SourceAddress, &tx.TargetAddress, &tx.Amount, &tx.Fee,
		&tx.ReceivedAmount, &tx.Token, &tx.Status,
		&sourceTx, &targetTx, &lockTx, &unlockTx, &tx.Nonce,
		&tx.CreatedAt, &tx.UpdatedAt, &confirmed, &errStr, &sigs)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("bridge: 查詢交易失敗: %w", err)
	}
	tx.SourceTxHash = sourceTx.String
	tx.TargetTxHash = targetTx.String
	tx.LockTxHash = lockTx.String
	tx.UnlockTxHash = unlockTx.String
	tx.Error = errStr.String
	tx.ConfirmedAt = confirmed.Int64
	if sigs.Valid && sigs.String != "" {
		_ = json.Unmarshal([]byte(sigs.String), &tx.Signatures)
	}
	return tx, nil
}

// GetBridgeTxsByAddress 查詢與地址相關的跨鏈交易。
func (s *Store) GetBridgeTxsByAddress(address string, limit int) ([]BridgeTx, error) {
	rows, err := s.db.Query(`SELECT bridge_tx_id FROM bridge_transactions
		WHERE source_address=? OR target_address=?
		ORDER BY created_at DESC LIMIT ?`, address, address, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// 先收集 id 並關閉 rows，再逐筆查詢（store 為單連接；結果集未關閉時再 Query 會自死鎖）。
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []BridgeTx
	for _, id := range ids {
		tx, err := s.GetBridgeTx(id)
		if err != nil {
			return nil, err
		}
		if tx != nil {
			out = append(out, *tx)
		}
	}
	return out, nil
}

// GetPendingBridgeTxs 返回 pending/locked 的交易。
func (s *Store) GetPendingBridgeTxs() ([]BridgeTx, error) {
	rows, err := s.db.Query(`SELECT bridge_tx_id FROM bridge_transactions
		WHERE status IN(?,?) ORDER BY created_at`,
		StatusPending, StatusLocked)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// 先收集 id 並關閉 rows，再逐筆查詢（store 為單連接；結果集未關閉時再 Query 會自死鎖）。
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []BridgeTx
	for _, id := range ids {
		tx, err := s.GetBridgeTx(id)
		if err != nil {
			return nil, err
		}
		if tx != nil {
			out = append(out, *tx)
		}
	}
	return out, nil
}

// AddValidator upsert 守衛驗證人。
func (s *Store) AddValidator(v Validator) error {
	active := 0
	if v.Active {
		active = 1
	}
	_, err := s.db.Exec(`INSERT OR REPLACE INTO bridge_validators
		(address,name,public_key,is_active,joined_at) VALUES(?,?,?,?,?)`,
		v.Address, v.Name, v.PublicKey, active, v.JoinedAt)
	return err
}

// GetActiveValidators 返回活躍守衛驗證人。
func (s *Store) GetActiveValidators() ([]Validator, error) {
	rows, err := s.db.Query(`SELECT address,name,public_key,is_active,joined_at
		FROM bridge_validators WHERE is_active=1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Validator
	for rows.Next() {
		var v Validator
		var active int
		if err := rows.Scan(&v.Address, &v.Name, &v.PublicKey,
			&active, &v.JoinedAt); err != nil {
			return nil, err
		}
		v.Active = active == 1
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetConfig 取配置項；不存在返回空串。
func (s *Store) GetConfig(key string) (string, error) {
	var v string
	err := s.db.QueryRow("SELECT value FROM bridge_config WHERE key=?",
		key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// SetConfig 寫配置項。
func (s *Store) SetConfig(key, value string) error {
	_, err := s.db.Exec(
		"INSERT OR REPLACE INTO bridge_config VALUES(?,?)", key, value)
	return err
}
