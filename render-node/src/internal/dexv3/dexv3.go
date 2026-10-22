// Package dexv3 實現 TAC Ai 智能鏈的 DEX V3 集中流動性做市（對標 Uniswap V3）。
//
// 核心模型：
//   - 每個流動性位置（Position，NFT 化 positionId）只在價格區間 [tickLower, tickUpper]
//     內提供流動性；價格 price = 1.0001^tick，以 √price（sqrtPrice, S）與流動性 L 做 swap。
//   - 費用分級三檔（單位 bps）：1（0.01%, tickSpacing=1）、5（0.05%, spacing=10）、
//     30（0.3%, spacing=60）。手續費從輸入量扣除，並以 feeGrowthGlobalX（每單位活躍
//     流動性累計）記帳；位置互動時惰性結算至 tokensOwedX，可 collect 領取。
//   - 本套件只維護純狀態與數學（對標 internal/ens）；使用者↔池帳戶的資金移動由
//     node handler 透過 wallet 服務完成。池帳戶名：dexv3_pool_<poolId>。
//
// 內部統一以「人類可讀浮點數」（float64）做價格/流動性/金額計算，避免 TACm(18) 與
// TIUSD(6) 小數位差異干擾；持久化於 SQLite（dexv3.db，WAL、單連線）。
package dexv3

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// 費用分級（單位 bps，1/10000）與對應 tickSpacing。
const (
	FeeTierVeryLow = 1  // 0.01%
	FeeTierLow     = 5  // 0.05%
	FeeTierHigh    = 30 // 0.3%
)

// tick 極限（Uniswap V3 常數），用於無下一個初始化 tick 時的對手價。
const (
	minTick = -887272
	maxTick = 887272
)

// Pool 流動池（token0/token1 為內建帳本資產名，如 TACm / TIUSD）。
type Pool struct {
	Id               int64   `json:"id"`
	Token0           string  `json:"token0"`
	Token1           string  `json:"token1"`
	FeeTier          int     `json:"fee_tier"`           // bps：1 / 5 / 30
	TickSpacing      int     `json:"tick_spacing"`       // 依費率分級
	SqrtPrice        float64 `json:"sqrt_price"`         // √price（price = 1.0001^tick）
	Tick             int     `json:"tick"`               // 當前 tick
	Liquidity        float64 `json:"liquidity"`          // 當前活躍流動性 L
	FeeGrowthGlobal0 float64 `json:"fee_growth_global0"` // token0 每單位 L 累計費用
	FeeGrowthGlobal1 float64 `json:"fee_growth_global1"` // token1 每單位 L 累計費用
}

// Position 集中流動性位置（NFT 化，positionId 唯一、由 owner 持有）。
type Position struct {
	Id                   int64   `json:"id"`
	Owner                string  `json:"owner"`
	PoolId               int64   `json:"pool_id"`
	TickLower            int     `json:"tick_lower"`
	TickUpper            int     `json:"tick_upper"`
	Liquidity            float64 `json:"liquidity"`
	FeeGrowthInside0Last float64 `json:"fee_growth_inside0_last"`
	FeeGrowthInside1Last float64 `json:"fee_growth_inside1_last"`
	TokensOwed0          float64 `json:"tokens_owed0"` // 可領取的 token0 費用
	TokensOwed1          float64 `json:"tokens_owed1"` // 可領取的 token1 費用
}

// MintResult mint / increase 的回傳。
type MintResult struct {
	PositionId int64   `json:"position_id"`
	Liquidity  float64 `json:"liquidity"` // 本次新增後位置總 L（increase 時為新增量後的總 L）
	Amount0    float64 `json:"amount0"`   // 實際投入的 token0
	Amount1    float64 `json:"amount1"`   // 實際投入的 token1
}

// DecreaseResult decrease（撤流）的回傳：退回給使用者的 token。
type DecreaseResult struct {
	PositionId         int64   `json:"position_id"`
	LiquidityRemaining float64 `json:"liquidity_remaining"`
	Amount0            float64 `json:"amount0"`
	Amount1            float64 `json:"amount1"`
}

// CollectResult collect（領費）的回傳。
type CollectResult struct {
	PositionId int64   `json:"position_id"`
	Amount0    float64 `json:"amount0"`
	Amount1    float64 `json:"amount1"`
}

// SwapResult swap 的回傳。
type SwapResult struct {
	PoolId     int64   `json:"pool_id"`
	ZeroForOne bool    `json:"zero_for_one"` // true=賣 token0 買 token1；false=賣 token1 買 token0
	AmountIn   float64 `json:"amount_in"`    // 實際耗用的輸入量（毛額，含費）
	AmountOut  float64 `json:"amount_out"`   // 輸出量
	Fee        float64 `json:"fee"`          // 手續費（與輸入同幣別）
	SqrtPrice  float64 `json:"sqrt_price"`   // 新 √price
	Tick       int     `json:"tick"`         // 新 tick
	Liquidity  float64 `json:"liquidity"`    // 新的活躍流動性
}

// Store DEX V3 狀態儲存。
type Store struct {
	db *sql.DB
	mu sync.Mutex
}

// Open 開啟 dexv3.db（WAL、單連線）並建表。
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("dexv3: 建立數據目錄: %w", err)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)",
		filepath.Join(dataDir, "dexv3.db"))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("dexv3: 開啟數據庫: %w", err)
	}
	db.SetMaxOpenConns(1)
	schema := `
CREATE TABLE IF NOT EXISTS dexv3_pools (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  token0 TEXT NOT NULL,
  token1 TEXT NOT NULL,
  fee_tier INTEGER NOT NULL,
  tick_spacing INTEGER NOT NULL,
  sqrt_price REAL NOT NULL,
  tick INTEGER NOT NULL,
  liquidity REAL NOT NULL DEFAULT 0,
  fee_growth_global0 REAL NOT NULL DEFAULT 0,
  fee_growth_global1 REAL NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_dexv3_pool_uniq ON dexv3_pools(token0, token1, fee_tier);
CREATE TABLE IF NOT EXISTS dexv3_positions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  owner TEXT NOT NULL,
  pool_id INTEGER NOT NULL,
  tick_lower INTEGER NOT NULL,
  tick_upper INTEGER NOT NULL,
  liquidity REAL NOT NULL,
  fee_growth_inside0_last REAL NOT NULL DEFAULT 0,
  fee_growth_inside1_last REAL NOT NULL DEFAULT 0,
  tokens_owed0 REAL NOT NULL DEFAULT 0,
  tokens_owed1 REAL NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_dexv3_pos_owner ON dexv3_positions(owner);
CREATE INDEX IF NOT EXISTS idx_dexv3_pos_pool ON dexv3_positions(pool_id);
CREATE TABLE IF NOT EXISTS dexv3_ticks (
  pool_id INTEGER NOT NULL,
  tick INTEGER NOT NULL,
  liquidity_net REAL NOT NULL DEFAULT 0,
  PRIMARY KEY (pool_id, tick)
);
`
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("dexv3: 初始化 schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close 關閉資料庫。
func (s *Store) Close() error { return s.db.Close() }

// tickSpacingForFeeTier 回傳費率分級對應的 tickSpacing（非法費率回錯）。
func tickSpacingForFeeTier(feeTier int) (int, error) {
	switch feeTier {
	case FeeTierVeryLow:
		return 1, nil
	case FeeTierLow:
		return 10, nil
	case FeeTierHigh:
		return 60, nil
	default:
		return 0, fmt.Errorf("dexv3: 不支援的費率分級 %d（僅 1 / 5 / 30 bps）", feeTier)
	}
}

// roundTick 將 tick 對齊 tickSpacing（向零截斷）。
func roundTick(tick, spacing int) int {
	if spacing <= 1 {
		return tick
	}
	return (tick / spacing) * spacing
}

// CreatePool 建立流動池。token0/token1 自動依字典序排序（小者為 token0）。
// sqrtPrice 為初始 √price（price = sqrtPrice^2 = 1.0001^tick）。
func (s *Store) CreatePool(token0, token1 string, feeTier int, sqrtPrice float64) (*Pool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token0 == "" || token1 == "" {
		return nil, fmt.Errorf("dexv3: token0/token1 不可為空")
	}
	if token0 == token1 {
		return nil, fmt.Errorf("dexv3: token0 不可等於 token1")
	}
	if token0 > token1 {
		token0, token1 = token1, token0
	}
	spacing, err := tickSpacingForFeeTier(feeTier)
	if err != nil {
		return nil, err
	}
	if sqrtPrice <= 0 || math.IsNaN(sqrtPrice) || math.IsInf(sqrtPrice, 0) {
		return nil, fmt.Errorf("dexv3: sqrtPrice 必須為正數")
	}
	tick := SqrtPriceToTick(sqrtPrice)
	// 對齊到 spacing 後再回推 sqrtPrice，保證初始 tick 落在整格。
	tick = roundTick(tick, spacing)
	sqrtPrice = TickToSqrtPrice(tick)

	// 防重複池。
	var exist int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM dexv3_pools WHERE token0=? AND token1=? AND fee_tier=?`,
		token0, token1, feeTier).Scan(&exist); err != nil {
		return nil, fmt.Errorf("dexv3: 檢查重複池: %w", err)
	}
	if exist > 0 {
		return nil, fmt.Errorf("dexv3: 池已存在（%s/%s @%dbps）", token0, token1, feeTier)
	}
	now := time.Now().Unix()
	res, err := s.db.Exec(`INSERT INTO dexv3_pools(token0,token1,fee_tier,tick_spacing,sqrt_price,tick,liquidity,fee_growth_global0,fee_growth_global1,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		token0, token1, feeTier, spacing, sqrtPrice, tick, 0.0, 0.0, 0.0, now)
	if err != nil {
		return nil, fmt.Errorf("dexv3: 建立池: %w", err)
	}
	id, _ := res.LastInsertId()
	return &Pool{
		Id: id, Token0: token0, Token1: token1, FeeTier: feeTier, TickSpacing: spacing,
		SqrtPrice: sqrtPrice, Tick: tick, Liquidity: 0,
	}, nil
}

// scanPool 從一列掃出 Pool。
func scanPool(row interface{ Scan(...any) error }) (*Pool, error) {
	var p Pool
	if err := row.Scan(&p.Id, &p.Token0, &p.Token1, &p.FeeTier, &p.TickSpacing,
		&p.SqrtPrice, &p.Tick, &p.Liquidity, &p.FeeGrowthGlobal0, &p.FeeGrowthGlobal1); err != nil {
		return nil, err
	}
	return &p, nil
}

const poolCols = `id, token0, token1, fee_tier, tick_spacing, sqrt_price, tick, liquidity, fee_growth_global0, fee_growth_global1`

// GetPool 依 id 取池。
func (s *Store) GetPool(poolId int64) (*Pool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := scanPool(s.db.QueryRow(`SELECT `+poolCols+` FROM dexv3_pools WHERE id=?`, poolId))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("dexv3: 池不存在（id=%d）", poolId)
	}
	if err != nil {
		return nil, fmt.Errorf("dexv3: 查詢池: %w", err)
	}
	return p, nil
}

// ListPools 列出全部池（最新建立在前）。
func (s *Store) ListPools() ([]Pool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT ` + poolCols + ` FROM dexv3_pools ORDER BY id DESC LIMIT 200`)
	if err != nil {
		return nil, fmt.Errorf("dexv3: 列出池: %w", err)
	}
	defer rows.Close()
	out := make([]Pool, 0, 8)
	for rows.Next() {
		p, err := scanPool(rows)
		if err != nil {
			return nil, fmt.Errorf("dexv3: 掃描池列: %w", err)
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// GetPosition 依 id 取位置。
func (s *Store) GetPosition(positionId int64) (*Position, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pos, err := scanPosition(s.db.QueryRow(`SELECT id,owner,pool_id,tick_lower,tick_upper,liquidity,fee_growth_inside0_last,fee_growth_inside1_last,tokens_owed0,tokens_owed1 FROM dexv3_positions WHERE id=?`, positionId))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("dexv3: 位置不存在（id=%d）", positionId)
	}
	if err != nil {
		return nil, fmt.Errorf("dexv3: 查詢位置: %w", err)
	}
	return pos, nil
}

func scanPosition(row interface{ Scan(...any) error }) (*Position, error) {
	var p Position
	if err := row.Scan(&p.Id, &p.Owner, &p.PoolId, &p.TickLower, &p.TickUpper, &p.Liquidity,
		&p.FeeGrowthInside0Last, &p.FeeGrowthInside1Last, &p.TokensOwed0, &p.TokensOwed1); err != nil {
		return nil, err
	}
	return &p, nil
}

// ListPositions 列出某 owner 的全部位置。
func (s *Store) ListPositions(owner string) ([]Position, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id,owner,pool_id,tick_lower,tick_upper,liquidity,fee_growth_inside0_last,fee_growth_inside1_last,tokens_owed0,tokens_owed1 FROM dexv3_positions WHERE owner=? ORDER BY id DESC LIMIT 200`, owner)
	if err != nil {
		return nil, fmt.Errorf("dexv3: 列出位置: %w", err)
	}
	defer rows.Close()
	out := make([]Position, 0, 8)
	for rows.Next() {
		p, err := scanPosition(rows)
		if err != nil {
			return nil, fmt.Errorf("dexv3: 掃描位置列: %w", err)
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}
