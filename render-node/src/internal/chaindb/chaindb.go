package chaindb

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// 貨幣政策：coinbase 區塊獎勵（指數減半，歸零後僅靠手續費）。
const (
	InitialBlockReward = 10.0    // 初始每塊增發 10 TACM
	HalvingInterval    = 5000000 // 每 500 萬塊減半
)

// 內存池參數。
const (
	MaxMempoolSize   = 5000
	MempoolExpireSec = 3600
)

// BlockReward 按高度計算 coinbase 獎勵。height<=0 返回 0；
// 每 HalvingInterval 塊減半，直到歸零。
func BlockReward(height int64) float64 {
	if height <= 0 {
		return 0.0
	}
	halvings := uint64(height) / HalvingInterval
	reward := InitialBlockReward
	for i := uint64(0); i < halvings; i++ {
		reward /= 2.0
	}
	if reward <= 0 {
		return 0.0
	}
	return reward
}

const schema = `
CREATE TABLE IF NOT EXISTS blocks (
    height INTEGER PRIMARY KEY,
    hash TEXT UNIQUE NOT NULL,
    prev_hash TEXT,
    merkle_root TEXT,
    proposer TEXT,
    proposer_address TEXT,
    ts INTEGER,
    tx_count INTEGER,
    difficulty INTEGER,
    nonce INTEGER,
    size INTEGER DEFAULT 0,
    base_fee TEXT,
    gas_used INTEGER DEFAULT 0,
    gas_limit INTEGER DEFAULT 0,
    burned TEXT
);
CREATE TABLE IF NOT EXISTS transactions (
    tx_hash TEXT PRIMARY KEY,
    block_height INTEGER,
    block_hash TEXT,
    tx_index INTEGER,
    from_addr TEXT,
    to_addr TEXT,
    amount TEXT,
    fee TEXT,
    gas_limit INTEGER DEFAULT 21000,
    max_fee TEXT,
    priority_fee TEXT,
    burned TEXT,
    nonce INTEGER,
    ts INTEGER,
    signature TEXT,
    pubkey TEXT,
    memo TEXT,
    status TEXT DEFAULT 'confirmed',
    FOREIGN KEY (block_height) REFERENCES blocks(height)
);
CREATE TABLE IF NOT EXISTS accounts (
    address TEXT PRIMARY KEY,
    balance TEXT DEFAULT '0',
    nonce INTEGER DEFAULT 0,
    pubkey TEXT,
    first_seen INTEGER,
    last_active INTEGER
);
CREATE TABLE IF NOT EXISTS mempool (
    tx_hash TEXT PRIMARY KEY,
    payload TEXT,
    signature TEXT,
    received_at INTEGER,
    fee TEXT
);
CREATE TABLE IF NOT EXISTS peers (
    node_id TEXT PRIMARY KEY,
    rpc_url TEXT,
    owner TEXT,
    last_seen INTEGER,
    online INTEGER DEFAULT 1,
    height INTEGER DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_tx_from ON transactions(from_addr);
CREATE INDEX IF NOT EXISTS idx_tx_to ON transactions(to_addr);
CREATE INDEX IF NOT EXISTS idx_tx_block ON transactions(block_height);
CREATE INDEX IF NOT EXISTS idx_blocks_hash ON blocks(hash);
CREATE TABLE IF NOT EXISTS logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tx_hash TEXT NOT NULL,
    block_height INTEGER NOT NULL,
    tx_index INTEGER DEFAULT 0,
    log_index INTEGER NOT NULL,
    address TEXT NOT NULL,
    topic0 TEXT,
    topic1 TEXT,
    topic2 TEXT,
    topic3 TEXT,
    data TEXT
);
CREATE INDEX IF NOT EXISTS idx_logs_tx ON logs(tx_hash);
CREATE INDEX IF NOT EXISTS idx_logs_addr ON logs(address);
CREATE INDEX IF NOT EXISTS idx_logs_topic0 ON logs(topic0);
CREATE INDEX IF NOT EXISTS idx_logs_block ON logs(block_height);
`

// ChainDB 為 TAC Ai 智能鏈的節點數據庫。
type ChainDB struct {
	db    *sql.DB
	path  string
	cache *ReadCache
}

// Open 打開（或創建）位於 dbPath 的數據庫，套用 WAL 與緩存參數並初始化表結構。
func Open(dbPath string) (*ChainDB, error) {
	if dir := filepath.Dir(dbPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("chaindb: 創建數據目錄失敗: %w", err)
		}
	}
	// busy_timeout 對應 Python timeout=30，避免寫入鎖定直接失敗。
	dsn := dbPath + "?_pragma=busy_timeout(30000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("chaindb: 打開數據庫失敗: %w", err)
	}
	// SQLite 單寫多讀，連接數過大反而加劇鎖競爭；保留少量連接。
	db.SetMaxOpenConns(8)

	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA cache_size=-64000",
		"PRAGMA temp_store=MEMORY",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("chaindb: 設置 %s 失敗: %w", p, err)
		}
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("chaindb: 初始化表結構失敗: %w", err)
	}
	// M75-2：舊庫相容 migration——transactions 補 gas_used 欄位（新庫 CREATE 已含）。
	if _, err := db.Exec("ALTER TABLE transactions ADD COLUMN gas_used INTEGER DEFAULT 0"); err != nil {
		// SQLite 不支援 ADD COLUMN IF NOT EXISTS；欄位已存在時報 duplicate column，忽略即可。
	}
	return &ChainDB{db: db, path: dbPath, cache: newReadCache()}, nil
}

// Close 關閉數據庫連接。
func (c *ChainDB) Close() error {
	return c.db.Close()
}

// ---- 數值格式化（對照 Python str(float)，最短往返） ----

// FormatFloat 標準化金額為固定小數字串（與帳本解析一致）。
// M35：改用 'f' 避免大數以科學記號輸出（1.18e+07 無法被金額解析器解析）。
func FormatFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0" // 整數值帶 ".0"（與 Python str(float) 一致）
	}
	return s
}

func parseFloat(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0.0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0.0
	}
	return f
}

func nowUnix() int64 { return time.Now().Unix() }

// ---- 區塊查詢 ----

func scanBlock(row interface {
	Scan(dest ...any) error
}) (*Block, error) {
	var b Block
	var prevHash sql.NullString
	if err := row.Scan(&b.Height, &b.Hash, &prevHash, &b.MerkleRoot, &b.Proposer,
		&b.ProposerAddress, &b.Ts, &b.TxCount, &b.Difficulty, &b.Nonce,
		&b.Size, &b.BaseFee, &b.GasUsed, &b.GasLimit, &b.Burned); err != nil {
		return nil, err
	}
	if prevHash.Valid {
		b.PrevHash = &prevHash.String
	}
	return &b, nil
}

const blockCols = "height, hash, prev_hash, merkle_root, proposer, proposer_address, ts, tx_count, difficulty, nonce, size, base_fee, gas_used, gas_limit, burned"

// GetLatestBlock 返回最高區塊；空庫返回 (nil, nil)。
func (c *ChainDB) GetLatestBlock() (*Block, error) {
	row := c.db.QueryRow("SELECT " + blockCols + " FROM blocks ORDER BY height DESC LIMIT 1")
	b, err := scanBlock(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("chaindb: 查最新塊失敗: %w", err)
	}
	return b, nil
}

// GetBlock 按高度取塊；不存在返回 (nil, nil)。
func (c *ChainDB) GetBlock(height int64) (*Block, error) {
	row := c.db.QueryRow("SELECT "+blockCols+" FROM blocks WHERE height = ?", height)
	b, err := scanBlock(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("chaindb: 查塊 %d 失敗: %w", height, err)
	}
	return b, nil
}

// GetBlockByHash 按哈希取塊；不存在返回 (nil, nil)。
func (c *ChainDB) GetBlockByHash(hash string) (*Block, error) {
	row := c.db.QueryRow("SELECT "+blockCols+" FROM blocks WHERE hash = ?", hash)
	b, err := scanBlock(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("chaindb: 按哈希查塊失敗: %w", err)
	}
	return b, nil
}

// GetBlockCount 返回區塊總數。
func (c *ChainDB) GetBlockCount() (int64, error) {
	var n int64
	if err := c.db.QueryRow("SELECT COUNT(*) FROM blocks").Scan(&n); err != nil {
		return 0, fmt.Errorf("chaindb: 統計區塊失敗: %w", err)
	}
	return n, nil
}

// GetTipHeight 返回最高區塊高度（空庫=0），為對外權威高度。
func (c *ChainDB) GetTipHeight() int64 {
	b, err := c.GetLatestBlock()
	if err != nil || b == nil {
		return 0
	}
	return b.Height
}

// GetBlocks 返回最近區塊（按高度降序），支持 limit/offset 分頁。
func (c *ChainDB) GetBlocks(limit, offset int) ([]*Block, error) {
	rows, err := c.db.Query("SELECT "+blockCols+
		" FROM blocks ORDER BY height DESC LIMIT ? OFFSET ?", limit, offset)
	if err != nil {
		return nil, fmt.Errorf("chaindb: 查區塊列表失敗: %w", err)
	}
	defer rows.Close()
	return collectBlocks(rows)
}

func collectBlocks(rows *sql.Rows) ([]*Block, error) {
	var out []*Block
	for rows.Next() {
		b, err := scanBlock(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
