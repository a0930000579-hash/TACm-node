// Package perp — TAC Ai 智能鏈永續合約（Perpetuals）模組，對標 dYdX / GMX。
//
// 本套件只維護純狀態與數學（對標 internal/ens）：
//   - 多空開倉 / 平倉 / 加減保證金 / 強平
//   - 標記價格由外部注入（PriceFn 或 SetExternalPrice）
//   - 未實現 PnL、維持保證金、強平價
//   - 資金費率（funding rate）惰性結算（每市場 lastFundingTs）
//
// 實際 TACm/TiUSD 進出由 node handler 用 wallet 服務處理；perp 帳本帳戶名為 "perp_vault"。
package perp

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// PriceFn 價格源：給回 symbol（例如 "BTC"）的標記價格。
type PriceFn func(symbol string) (float64, bool)

// Market 永續市場。
type Market struct {
	Symbol            string  `json:"symbol"`
	MarkPrice         float64 `json:"mark_price"`          // 標記價格（USD/基礎幣）
	IndexPrice       float64 `json:"index_price"`         // 指數價格
	FundingRate       float64 `json:"funding_rate"`        // 每小時資金費率（小數，例如 0.0001 = 0.01%）
	LastFundingTs     int64   `json:"last_funding_ts"`     // 上次結算時間（Unix 秒）
	OpenInterestLong  float64 `json:"open_interest_long"`  // 多頭未平倉（基礎幣數量）
	OpenInterestShort float64 `json:"open_interest_short"` // 空頭未平倉
	MaintMarginBps    int64   `json:"maint_margin_bps"`    // 維持保證金率（bps，10000=100%）
	MaxLeverage      int64   `json:"max_leverage"`        // 最大槓桿
}

// Position 單一永續持倉。
type Position struct {
	ID             int64   `json:"id"`
	Trader         string  `json:"trader"`
	Symbol         string  `json:"symbol"`
	Side           string  `json:"side"` // long / short
	Size           float64 `json:"size"`
	EntryPrice     float64 `json:"entry_price"`
	Margin         float64 `json:"margin"`          // 已鎖定保證金（以 MarginAsset 計）
	MarginAsset    string  `json:"margin_asset"`    // TACm / TIUSD
	Leverage       float64 `json:"leverage"`       // 實際槓桿
	LastFundingTs  int64   `json:"last_funding_ts"` // 該倉位上次結算時間
	Open           bool    `json:"open"`
	OpenedAt       int64   `json:"opened_at"`
	// 動態計算欄位（不存庫）：
	MarkPrice      float64 `json:"mark_price,omitempty"`
	UnrealizedPnL  float64 `json:"unrealized_pnl,omitempty"`
	MaintMargin    float64 `json:"maint_margin,omitempty"`
	LiqPrice       float64 `json:"liq_price,omitempty"`
}

// Account perp 保證金帳戶（每 trader 一個）。
type Account struct {
	Trader   string             `json:"trader"`
	Balances map[string]float64 `json:"balances"` // asset → 可用保證金
	// 動態計算：
	Equity       float64 `json:"equity,omitempty"`        // 可用淨值 + 所有未平倉未實現 PnL
	TotalMargin  float64 `json:"total_margin,omitempty"`   // 所有未平倉已鎖定保證金合計（以主帳戶資產加總，簡化）
}

// Store perp 狀態儲存。
type Store struct {
	db      *sql.DB
	mu      sync.Mutex
	priceMu sync.RWMutex
	priceFn PriceFn
	// 外部推價快取（handler 用 SetExternalPrice 推入）。
	extPrices   map[string]float64
	extPriceMu  sync.RWMutex
}

// Open 開啟 perp.db（WAL、單連線）並建表。
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("perp: 建立數據目錄: %w", err)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)",
		filepath.Join(dataDir, "perp.db"))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("perp: 開啟數據庫: %w", err)
	}
	db.SetMaxOpenConns(1)
	schema := `
CREATE TABLE IF NOT EXISTS perp_markets (
  symbol TEXT PRIMARY KEY,
  mark_price REAL NOT NULL DEFAULT 0,
  index_price REAL NOT NULL DEFAULT 0,
  funding_rate REAL NOT NULL DEFAULT 0,
  last_funding_ts INTEGER NOT NULL DEFAULT 0,
  oi_long REAL NOT NULL DEFAULT 0,
  oi_short REAL NOT NULL DEFAULT 0,
  maint_margin_bps INTEGER NOT NULL DEFAULT 50,
  max_leverage INTEGER NOT NULL DEFAULT 20
);
CREATE TABLE IF NOT EXISTS perp_positions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  trader TEXT NOT NULL,
  symbol TEXT NOT NULL,
  side TEXT NOT NULL,
  size REAL NOT NULL,
  entry_price REAL NOT NULL,
  margin REAL NOT NULL,
  margin_asset TEXT NOT NULL,
  leverage REAL NOT NULL,
  last_funding_ts INTEGER NOT NULL,
  open INTEGER NOT NULL DEFAULT 1,
  opened_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_perp_pos_trader ON perp_positions(trader, open);
CREATE INDEX IF NOT EXISTS idx_perp_pos_symbol ON perp_positions(symbol, open);
CREATE TABLE IF NOT EXISTS perp_accounts (
  trader TEXT NOT NULL,
  asset TEXT NOT NULL,
  balance REAL NOT NULL DEFAULT 0,
  PRIMARY KEY(trader, asset)
);
`
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("perp: 初始化 schema: %w", err)
	}
	s := &Store{db: db, extPrices: make(map[string]float64)}
	// 預設市場（BTC/ETH/SOL），若不存在則建立。
	_ = s.ensureMarketLocked("BTC", 50, 20)
	_ = s.ensureMarketLocked("ETH", 50, 20)
	_ = s.ensureMarketLocked("SOL", 100, 10)
	return s, nil
}

// Close 關閉資料庫。
func (s *Store) Close() error { return s.db.Close() }

// SetPriceFn 注入價格源（node 層從 oracle 接）。
func (s *Store) SetPriceFn(fn PriceFn) {
	s.priceMu.Lock()
	defer s.priceMu.Unlock()
	s.priceFn = fn
}

// SetExternalPrice 由 handler（node 層）推入最新價格（通常先從 oracle 取到）。
func (s *Store) SetExternalPrice(symbol string, price float64) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" || price <= 0 || math.IsNaN(price) || math.IsInf(price, 0) {
		return
	}
	s.extPriceMu.Lock()
	s.extPrices[symbol] = price
	s.extPriceMu.Unlock()
	// 同時更新市場 mark_price（若市場已存在）。
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.db.Exec(`UPDATE perp_markets SET mark_price=?, index_price=? WHERE symbol=?`, price, price, symbol)
}

// priceLocked 取標記價格：優先 extPrices → priceFn → 市場內存值。
// 呼叫者必須持有 s.mu。
func (s *Store) priceLocked(symbol string) (float64, bool) {
	s.extPriceMu.RLock()
	p, ok := s.extPrices[symbol]
	s.extPriceMu.RUnlock()
	if ok && p > 0 {
		return p, true
	}
	s.priceMu.RLock()
	fn := s.priceFn
	s.priceMu.RUnlock()
	if fn != nil {
		if p, ok := fn(symbol); ok && p > 0 {
			return p, true
		}
	}
	// 退化：市場內存值。
	var mp float64
	err := s.db.QueryRow(`SELECT mark_price FROM perp_markets WHERE symbol=?`, symbol).Scan(&mp)
	if err == nil && mp > 0 {
		return mp, true
	}
	return 0, false
}

// EnsureMarket 建立或更新市場參數（maintBps、maxLeverage）。
func (s *Store) EnsureMarket(symbol string, maintBps, maxLeverage int64) error {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return fmt.Errorf("perp: 市場符號為空")
	}
	if maintBps < 1 || maintBps > 10000 {
		return fmt.Errorf("perp: 維持保證金率須在 1~10000 bps")
	}
	if maxLeverage < 1 || maxLeverage > 100 {
		return fmt.Errorf("perp: 最大槓桿須在 1~100x")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ensureMarketLocked(symbol, maintBps, maxLeverage)
}

func (s *Store) ensureMarketLocked(symbol string, maintBps, maxLeverage int64) error {
	var ts int64
	err := s.db.QueryRow(`SELECT last_funding_ts FROM perp_markets WHERE symbol=?`, symbol).Scan(&ts)
	if err == sql.ErrNoRows {
		now := time.Now().Unix()
		_, err := s.db.Exec(`INSERT INTO perp_markets(symbol,last_funding_ts,maint_margin_bps,max_leverage) VALUES(?,?,?,?)`,
			symbol, now, maintBps, maxLeverage)
		if err != nil {
			return fmt.Errorf("perp: 建立市場: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("perp: 查詢市場: %w", err)
	}
	_, _ = s.db.Exec(`UPDATE perp_markets SET maint_margin_bps=?, max_leverage=? WHERE symbol=?`, maintBps, maxLeverage, symbol)
	return nil
}

// loadMarketLocked 從 DB 讀出市場。
func (s *Store) loadMarketLocked(symbol string) (Market, error) {
	var m Market
	err := s.db.QueryRow(`SELECT symbol, mark_price, index_price, funding_rate, last_funding_ts, oi_long, oi_short, maint_margin_bps, max_leverage FROM perp_markets WHERE symbol=?`, symbol).
		Scan(&m.Symbol, &m.MarkPrice, &m.IndexPrice, &m.FundingRate, &m.LastFundingTs, &m.OpenInterestLong, &m.OpenInterestShort, &m.MaintMarginBps, &m.MaxLeverage)
	if err == sql.ErrNoRows {
		return Market{}, fmt.Errorf("perp: 市場不存在: %s", symbol)
	}
	if err != nil {
		return Market{}, fmt.Errorf("perp: 讀取市場: %w", err)
	}
	return m, nil
}

// marketsLocked 列出全部市場。
func (s *Store) marketsLocked() ([]Market, error) {
	rows, err := s.db.Query(`SELECT symbol, mark_price, index_price, funding_rate, last_funding_ts, oi_long, oi_short, maint_margin_bps, max_leverage FROM perp_markets ORDER BY symbol`)
	if err != nil {
		return nil, fmt.Errorf("perp: 列出市場: %w", err)
	}
	defer rows.Close()
	out := make([]Market, 0, 8)
	for rows.Next() {
		var m Market
		if err := rows.Scan(&m.Symbol, &m.MarkPrice, &m.IndexPrice, &m.FundingRate, &m.LastFundingTs, &m.OpenInterestLong, &m.OpenInterestShort, &m.MaintMarginBps, &m.MaxLeverage); err != nil {
			return nil, fmt.Errorf("perp: 掃描市場: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
