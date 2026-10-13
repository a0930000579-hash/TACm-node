package pos

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	_ "modernc.org/sqlite"
)

// Store 為 PoS 質押存儲。寫操作由 mu 串行化，並發安全。
type Store struct {
	db  *sql.DB
	cfg Config
}

// Open 打開（必要時創建）PoS 數據庫並初始化 schema。
func Open(path string, cfg Config) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("pos: 創建目錄失敗: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("pos: 打開數據庫失敗: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec("PRAGMA synchronous=NORMAL"); err != nil {
		db.Close()
		return nil, err
	}
	// M87：相容既有 db 的 pubkey_hex 欄位遷移（已存在時忽略錯誤）。
	_, _ = db.Exec(`ALTER TABLE validators ADD COLUMN pubkey_hex TEXT`)
	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS slash_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		address TEXT NOT NULL,
		amount REAL NOT NULL,
		rate REAL NOT NULL,
		reason TEXT,
		created_at INTEGER NOT NULL)`)

	if _, err := db.Exec("PRAGMA busy_timeout=30000"); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db, cfg: cfg}
	if err := s.initSchema(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close 關閉數據庫。
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) initSchema() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS stakes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			uid INTEGER NOT NULL,
			address TEXT NOT NULL,
			amount REAL NOT NULL,
			status TEXT DEFAULT 'active',
			staked_at INTEGER NOT NULL,
			unlock_requested_at INTEGER,
			unlocked_at INTEGER,
			last_reward_at INTEGER,
			total_earned REAL DEFAULT 0,
			validator_id INTEGER)`,
		`CREATE TABLE IF NOT EXISTS validators (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			uid INTEGER NOT NULL,
			address TEXT UNIQUE NOT NULL,
			name TEXT,
			total_stake REAL DEFAULT 0,
			self_stake REAL DEFAULT 0,
			delegated_stake REAL DEFAULT 0,
			commission_rate REAL DEFAULT 0.05,
			status TEXT DEFAULT 'active',
			joined_at INTEGER NOT NULL,
			jailed_at INTEGER,
			uptime REAL DEFAULT 100.0,
			blocks_proposed INTEGER DEFAULT 0,
			blocks_missed INTEGER DEFAULT 0,
			pubkey_hex TEXT)`,
		`CREATE TABLE IF NOT EXISTS slash_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			address TEXT NOT NULL,
			amount REAL NOT NULL,
			rate REAL NOT NULL,
			reason TEXT,
			created_at INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS rewards (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			uid INTEGER NOT NULL,
			stake_id INTEGER,
			amount REAL NOT NULL,
			reward_type TEXT DEFAULT 'staking',
			distributed_at INTEGER NOT NULL,
			block_height INTEGER)`,
		`CREATE TABLE IF NOT EXISTS unlock_requests (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			stake_id INTEGER NOT NULL,
			uid INTEGER NOT NULL,
			amount REAL NOT NULL,
			requested_at INTEGER NOT NULL,
			unlock_at INTEGER NOT NULL,
			status TEXT DEFAULT 'pending',
			completed_at INTEGER)`,
		`CREATE TABLE IF NOT EXISTS pos_params (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at INTEGER)`,
		`CREATE INDEX IF NOT EXISTS idx_stakes_uid ON stakes(uid)`,
		`CREATE INDEX IF NOT EXISTS idx_stakes_status ON stakes(status)`,
		`CREATE INDEX IF NOT EXISTS idx_stakes_address ON stakes(address)`,
		`CREATE INDEX IF NOT EXISTS idx_validators_status ON validators(status)`,
		`CREATE INDEX IF NOT EXISTS idx_rewards_uid ON rewards(uid)`,
		`CREATE INDEX IF NOT EXISTS idx_unlock_uid ON unlock_requests(uid)`,
	}
	for _, st := range stmts {
		if _, err := s.db.Exec(st); err != nil {
			return fmt.Errorf("pos: 建表失敗: %w", err)
		}
	}
	return s.initParams()
}

func (s *Store) initParams() error {
	now := time.Now().Unix()
	defaults := map[string]string{
		"total_staked":              "0",
		"total_validators":          "0",
		"total_rewards_distributed": "0",
		"last_reward_distribution":  "0",
		"annual_yield_rate":         strconv.FormatFloat(s.cfg.AnnualYield, 'f', -1, 64),
		"unlock_period":             strconv.FormatInt(s.cfg.UnlockPeriod, 10),
	}
	for k, v := range defaults {
		if _, err := s.db.Exec(
			`INSERT OR IGNORE INTO pos_params (key,value,updated_at) VALUES (?,?,?)`,
			k, v, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) getParam(key, def string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM pos_params WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return def, nil
	}
	if err != nil {
		return "", err
	}
	return v, nil
}

func (s *Store) setParam(key, value string) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO pos_params (key,value,updated_at) VALUES (?,?,?)`,
		key, value, time.Now().Unix())
	return err
}
