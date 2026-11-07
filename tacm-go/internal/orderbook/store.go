package orderbook

// store.go — DDL 與 SQL 存取（開放單行查詢／寫入，均在 Store.mu 保護下）。

import (
	"database/sql"
	"fmt"
	"time"
)

const schemaDDL = `
CREATE TABLE IF NOT EXISTS ob_markets (
  symbol TEXT PRIMARY KEY,
  base TEXT NOT NULL,
  quote TEXT NOT NULL,
  min_amount REAL NOT NULL DEFAULT 0.001,
  price_tick REAL NOT NULL DEFAULT 0.0001
);

CREATE TABLE IF NOT EXISTS ob_orders (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  market TEXT NOT NULL,
  maker TEXT NOT NULL,
  side TEXT NOT NULL,
  type TEXT NOT NULL DEFAULT 'limit',
  price REAL NOT NULL,
  amount REAL NOT NULL,
  filled REAL NOT NULL DEFAULT 0,
  fee REAL NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'open',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ob_orders_market ON ob_orders(market, status, side, price, id);
CREATE INDEX IF NOT EXISTS idx_ob_orders_maker ON ob_orders(maker, id DESC);

CREATE TABLE IF NOT EXISTS ob_trades (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  market TEXT NOT NULL,
  taker TEXT NOT NULL,
  maker TEXT NOT NULL,
  taker_order_id INTEGER NOT NULL,
  maker_order_id INTEGER NOT NULL,
  taker_side TEXT NOT NULL,
  price REAL NOT NULL,
  amount REAL NOT NULL,
  taker_fee REAL NOT NULL DEFAULT 0,
  maker_fee REAL NOT NULL DEFAULT 0,
  time INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ob_trades_market ON ob_trades(market, id DESC);
CREATE INDEX IF NOT EXISTS idx_ob_trades_taker ON ob_trades(taker, id DESC);
CREATE INDEX IF NOT EXISTS idx_ob_trades_maker ON ob_trades(maker, id DESC);
`

// scanOrder 從 rows/row 掃描一列 Order。
func scanOrder(scanner interface{ Scan(...any) error }) (*Order, error) {
	var o Order
	if err := scanner.Scan(&o.ID, &o.Market, &o.Maker, &o.Side, &o.Type, &o.Price,
		&o.Amount, &o.Filled, &o.Fee, &o.Status, &o.CreatedAt, &o.UpdatedAt); err != nil {
		return nil, err
	}
	return &o, nil
}

const orderCols = `id, market, maker, side, type, price, amount, filled, fee, status, created_at, updated_at`

// getOrderLocked 讀單；呼叫方須持鎖。不存在回錯。
func (s *Store) getOrderLocked(id int64) (*Order, error) {
	row := s.db.QueryRow(`SELECT `+orderCols+` FROM ob_orders WHERE id=?`, id)
	o, err := scanOrder(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("orderbook: 訂單不存在 (id=%d)", id)
	}
	if err != nil {
		return nil, fmt.Errorf("orderbook: 讀取訂單: %w", err)
	}
	return o, nil
}

// GetOrder 公開：依 id 讀單。
func (s *Store) GetOrder(id int64) (*Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getOrderLocked(id)
}

// Market 公開：依 symbol 讀市場（不存在回錯）。
func (s *Store) Market(symbol string) (*Market, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.marketLocked(symbol)
}

func (s *Store) marketLocked(symbol string) (*Market, error) {
	if m, ok := s.markets[symbol]; ok {
		return &m, nil
	}
	// 也查 DB（允許外部 INSERT 自定義市場）。
	row := s.db.QueryRow(`SELECT symbol, base, quote, min_amount, price_tick FROM ob_markets WHERE symbol=?`, symbol)
	var m Market
	if err := row.Scan(&m.Symbol, &m.Base, &m.Quote, &m.MinAmount, &m.PriceTick); err != nil {
		return nil, fmt.Errorf("orderbook: 市場不存在 %q", symbol)
	}
	return &m, nil
}

// Markets 列出所有市場。
func (s *Store) Markets() ([]Market, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT symbol, base, quote, min_amount, price_tick FROM ob_markets ORDER BY symbol`)
	if err != nil {
		return nil, fmt.Errorf("orderbook: 列出市場: %w", err)
	}
	defer rows.Close()
	out := make([]Market, 0, 4)
	for rows.Next() {
		var m Market
		if err := rows.Scan(&m.Symbol, &m.Base, &m.Quote, &m.MinAmount, &m.PriceTick); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MyOrders 列出 maker 的訂單（新→舊，limit 上限 200）。
func (s *Store) MyOrders(maker, market string, limit int) ([]Order, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	q := `SELECT ` + orderCols + ` FROM ob_orders WHERE maker=?`
	args := []any{maker}
	if market != "" {
		q += ` AND market=?`
		args = append(args, market)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("orderbook: 查詢我的訂單: %w", err)
	}
	defer rows.Close()
	out := make([]Order, 0, 8)
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

// RecentTrades 列出某市場最近成交（新→舊）。
func (s *Store) RecentTrades(market string, limit int) ([]Fill, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id, market, taker, maker, taker_order_id, maker_order_id,
		taker_side, price, amount, taker_fee, maker_fee, time
		FROM ob_trades WHERE market=? ORDER BY id DESC LIMIT ?`, market, limit)
	if err != nil {
		return nil, fmt.Errorf("orderbook: 查詢成交: %w", err)
	}
	defer rows.Close()
	out := make([]Fill, 0, 8)
	for rows.Next() {
		var f Fill
		if err := rows.Scan(&f.ID, &f.Market, &f.Taker, &f.Maker, &f.TakerOrderID, &f.MakerOrderID,
			&f.TakerSide, &f.Price, &f.Amount, &f.TakerFee, &f.MakerFee, &f.Time); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// nowSec 回傳目前 Unix 秒（測試可間接受時間影響；這裡直接用 time.Now）。
func nowSec() int64 { return time.Now().Unix() }
