package exchange

import (
	"database/sql"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	_ "modernc.org/sqlite"
)

// Store 為交易所 SQLite 持久化：多資產餘額簿（avail/locked）、掛單、成交。
// 與錢包/鏈存儲一致：單連接、參數化、寫入走事務。
// 資產餘額一律 TEXT 存最小單位十進制（big.Int 防溢出）；SQLite 本身不做
// 大數運算，所有加減在 Go 側讀-算-寫（單連接序列化保證原子）。
type Store struct {
	db *sql.DB
}

const exSchema = `
CREATE TABLE IF NOT EXISTS ex_balances (
    uid   TEXT NOT NULL,
    asset TEXT NOT NULL,
    avail TEXT NOT NULL DEFAULT '0',
    locked TEXT NOT NULL DEFAULT '0',
    PRIMARY KEY (uid, asset)
);
CREATE TABLE IF NOT EXISTS ex_orders (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    uid TEXT NOT NULL,
    market TEXT NOT NULL,
    side TEXT NOT NULL,
    type TEXT NOT NULL DEFAULT 'limit',
    price TEXT NOT NULL,
    qty TEXT NOT NULL,
    filled TEXT NOT NULL DEFAULT '0',
    status TEXT NOT NULL DEFAULT 'open',
    created_ts INTEGER NOT NULL,
    base TEXT NOT NULL,
    quote TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS ex_trades (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    market TEXT NOT NULL,
    ts INTEGER NOT NULL,
    taker_side TEXT NOT NULL,
    price TEXT NOT NULL,
    qty TEXT NOT NULL,
    quote TEXT NOT NULL,
    taker_fee TEXT NOT NULL,
    maker_fee TEXT NOT NULL,
    taker_uid TEXT NOT NULL,
    maker_uid TEXT NOT NULL,
    taker_order_id INTEGER NOT NULL,
    maker_order_id INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ex_orders_uid ON ex_orders(uid);
CREATE INDEX IF NOT EXISTS idx_ex_orders_market ON ex_orders(market, status);
CREATE INDEX IF NOT EXISTS idx_ex_trades_market ON ex_trades(market, id DESC);
`

// Open 打開（或創建）交易所帳本。
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("exchange: 建立目錄: %w", err)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)",
		filepath.Join(dataDir, "exchange.db"))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("exchange: 開啟資料庫: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(exSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("exchange: 初始化 schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close 關閉。
func (s *Store) Close() error { return s.db.Close() }

// Balance 返回某帳戶某資產餘額（不存在返回零）。
type Balance struct {
	UID       string `json:"uid"`
	Asset     Asset  `json:"asset"`
	Avail     Amount `json:"-"`
	Locked    Amount `json:"-"`
	AvailStr  string `json:"avail"`
	LockedStr string `json:"locked"`
}

func (s *Store) Balance(uid string, a Asset) (*Balance, error) {
	row := s.db.QueryRow(`SELECT uid, asset, avail, locked FROM ex_balances WHERE uid = ? AND asset = ?`, uid, string(a))
	b := &Balance{UID: uid, Asset: a}
	var avail, locked string
	err := row.Scan(&b.UID, &b.Asset, &avail, &locked)
	if err == sql.ErrNoRows {
		b.AvailStr, b.LockedStr = "0", "0"
		return b, nil
	}
	if err != nil {
		return nil, fmt.Errorf("exchange: 讀取餘額: %w", err)
	}
	b.Avail, b.Locked = parseAmt(avail, a), parseAmt(locked, a)
	b.AvailStr, b.LockedStr = b.Avail.String(a), b.Locked.String(a)
	return b, nil
}

// Balances 返回帳戶全部資產餘額。
func (s *Store) Balances(uid string) ([]*Balance, error) {
	rows, err := s.db.Query(`SELECT uid, asset, avail, locked FROM ex_balances WHERE uid = ?`, uid)
	if err != nil {
		return nil, fmt.Errorf("exchange: 讀取餘額列表: %w", err)
	}
	defer rows.Close()
	out := []*Balance{}
	for rows.Next() {
		b := &Balance{UID: uid}
		var asset, avail, locked string
		if err := rows.Scan(&b.UID, &asset, &avail, &locked); err != nil {
			return nil, err
		}
		b.Asset = Asset(asset)
		b.Avail, b.Locked = parseAmt(avail, b.Asset), parseAmt(locked, b.Asset)
		b.AvailStr, b.LockedStr = b.Avail.String(b.Asset), b.Locked.String(b.Asset)
		out = append(out, b)
	}
	return out, rows.Err()
}

// credit / debit 對 avail 加/減；debit 需餘額充足。
func (s *Store) credit(uid string, a Asset, amt Amount) error {
	return s.adjust(uid, a, amt, true)
}
func (s *Store) debit(uid string, a Asset, amt Amount) error {
	return s.adjust(uid, a, amt, false)
}

func (s *Store) adjust(uid string, a Asset, amt Amount, add bool) error {
	if amt.IsZero() {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.adjustTx(tx, uid, a, amt, add); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) adjustTx(tx *sql.Tx, uid string, a Asset, amt Amount, add bool) error {
	var cur string
	err := tx.QueryRow(`SELECT avail FROM ex_balances WHERE uid = ? AND asset = ?`, uid, string(a)).Scan(&cur)
	if err == sql.ErrNoRows {
		cur = "0"
	}
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("exchange: 讀取餘額: %w", err)
	}
	bal := parseAmt(cur, a)
	next := bal
	if add {
		next = bal.Add(amt)
	} else {
		next = bal.Sub(amt)
		if next.Cmp(Amount{}) < 0 {
			return fmt.Errorf("exchange: 餘額不足 %s %s (avail %s, 需 %s)", uid, a, bal.String(a), amt.String(a))
		}
	}
	if _, err := tx.Exec(
		`INSERT INTO ex_balances (uid, asset, avail, locked) VALUES (?, ?, ?, '0')
		 ON CONFLICT(uid, asset) DO UPDATE SET avail = excluded.avail`,
		uid, string(a), next.ToBig().String()); err != nil {
		return fmt.Errorf("exchange: 更新餘額: %w", err)
	}
	return nil
}

// lock 鎖定資產：avail -= amt, locked += amt（不足報錯）。
func (s *Store) lock(uid string, a Asset, amt Amount) error {
	return s.moveLock(uid, a, amt, true)
}

// unlock 解鎖資產：avail += amt, locked -= amt。
func (s *Store) unlock(uid string, a Asset, amt Amount) error {
	return s.moveLock(uid, a, amt, false)
}

// consumeLocked 消耗鎖定餘額（成交已兌付，鎖定不歸還 avail）。
func (s *Store) consumeLocked(uid string, a Asset, amt Amount) error {
	if amt.IsZero() {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("exchange: 開啟事務: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var curA, curL string
	err = tx.QueryRow(`SELECT avail, locked FROM ex_balances WHERE uid = ? AND asset = ?`, uid, string(a)).Scan(&curA, &curL)
	if err == sql.ErrNoRows {
		return fmt.Errorf("exchange: 餘額不存在 %s %s", uid, a)
	}
	if err != nil {
		return fmt.Errorf("exchange: 讀取餘額: %w", err)
	}
	bal := parseAmt(curA, a)
	locked := parseAmt(curL, a)
	if locked.Cmp(amt) < 0 {
		return fmt.Errorf("exchange: 鎖定餘額不足 %s %s (locked %s, 需扣 %s)", uid, a, locked.String(a), amt.String(a))
	}
	nextL := locked.Sub(amt)
	if _, err := tx.Exec(
		`UPDATE ex_balances SET locked = ? WHERE uid = ? AND asset = ?`,
		nextL.ToBig().String(), uid, string(a)); err != nil {
		return fmt.Errorf("exchange: 更新鎖定: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("exchange: 提交: %w", err)
	}
	_ = bal
	return nil
}

func (s *Store) moveLock(uid string, a Asset, amt Amount, lock bool) error {
	if amt.IsZero() {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var curA, curL string
	err = tx.QueryRow(`SELECT avail, locked FROM ex_balances WHERE uid = ? AND asset = ?`, uid, string(a)).Scan(&curA, &curL)
	if err == sql.ErrNoRows {
		curA, curL = "0", "0"
	}
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("exchange: 讀取餘額: %w", err)
	}
	avail, locked := parseAmt(curA, a), parseAmt(curL, a)
	var newA, newL Amount
	if lock {
		newA, newL = avail.Sub(amt), locked.Add(amt)
		if newA.Cmp(Amount{}) < 0 {
			return fmt.Errorf("exchange: 可用餘額不足 %s %s (avail %s, 需鎖 %s)", uid, a, avail.String(a), amt.String(a))
		}
	} else {
		newA, newL = avail.Add(amt), locked.Sub(amt)
		if newL.Cmp(Amount{}) < 0 {
			return fmt.Errorf("exchange: 鎖定餘額不足 %s %s (locked %s, 需解 %s)", uid, a, locked.String(a), amt.String(a))
		}
	}
	if _, err := tx.Exec(
		`INSERT INTO ex_balances (uid, asset, avail, locked) VALUES (?, ?, ?, ?)
		 ON CONFLICT(uid, asset) DO UPDATE SET avail = excluded.avail, locked = excluded.locked`,
		uid, string(a), newA.ToBig().String(), newL.ToBig().String()); err != nil {
		return fmt.Errorf("exchange: 更新鎖定: %w", err)
	}
	return tx.Commit()
}

// insertOrder 持久化新掛單（回傳自增 ID）。
func (s *Store) insertOrder(o *Order) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO ex_orders (uid, market, side, type, price, qty, filled, status, created_ts, base, quote)
		 VALUES (?, ?, ?, ?, ?, ?, '0', ?, ?, ?, ?)`,
		o.UID, o.Market, string(o.Side), string(o.Type),
		o.Price.ToBig().String(), o.Qty.ToBig().String(), string(o.Status),
		o.CreatedTs, string(o.QuoteBase), string(o.QuoteQuote))
	if err != nil {
		return 0, fmt.Errorf("exchange: 寫掛單: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, nil
}

// getOrder 讀取掛單。
func (s *Store) getOrder(id int64) (*Order, error) {
	row := s.db.QueryRow(
		`SELECT id, uid, market, side, type, price, qty, filled, status, created_ts, base, quote
		 FROM ex_orders WHERE id = ?`, id)
	o := &Order{}
	var side, typ, status, price, qty, filled, base, quote string
	err := row.Scan(&o.ID, &o.UID, &o.Market, &side, &typ, &price, &qty, &filled, &status, &o.CreatedTs, &base, &quote)
	if err != nil {
		return nil, fmt.Errorf("exchange: 讀取掛單 %d: %w", id, err)
	}
	o.Side, o.Type, o.Status = Side(side), OrderType(typ), OrderStatus(status)
	o.QuoteBase, o.QuoteQuote = Asset(base), Asset(quote)
	o.Price, o.Qty, o.Filled = parseAmt(price, o.QuoteQuote), parseAmt(qty, o.QuoteBase), parseAmt(filled, o.QuoteBase)
	o.PriceStr, o.QtyStr, o.FilledStr = o.Price.String(o.QuoteQuote), o.Qty.String(o.QuoteBase), o.Filled.String(o.QuoteBase)
	o.RemainingStr = o.remaining().String(o.QuoteBase)
	return o, nil
}

// updateOrder 更新掛單狀態/成交量。
func (s *Store) updateOrder(o *Order) error {
	if _, err := s.db.Exec(
		`UPDATE ex_orders SET filled = ?, status = ? WHERE id = ?`,
		o.Filled.ToBig().String(), string(o.Status), o.ID); err != nil {
		return fmt.Errorf("exchange: 更新掛單 %d: %w", o.ID, err)
	}
	return nil
}

// cancelOrder 標記撤單（僅限 open/partially_filled）。
func (s *Store) cancelOrder(id int64) (bool, error) {
	res, err := s.db.Exec(
		`UPDATE ex_orders SET status = 'canceled' WHERE id = ? AND status IN ('open', 'partially_filled')`, id)
	if err != nil {
		return false, fmt.Errorf("exchange: 撤單 %d: %w", id, err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// openOrders 讀取某帳戶未成交訂單。
func (s *Store) openOrders(uid string, market string) ([]*Order, error) {
	rows, err := s.db.Query(
		`SELECT id, uid, market, side, type, price, qty, filled, status, created_ts, base, quote
		 FROM ex_orders WHERE uid = ? AND market = ? AND status IN ('open', 'partially_filled')
		 ORDER BY created_ts`, uid, market)
	if err != nil {
		return nil, fmt.Errorf("exchange: 讀取掛單列表: %w", err)
	}
	defer rows.Close()
	return scanOrders(rows)
}

// scanOrders 掃描訂單行。
func scanOrders(rows *sql.Rows) ([]*Order, error) {
	out := []*Order{}
	for rows.Next() {
		o := &Order{}
		var side, typ, status, price, qty, filled, base, quote string
		if err := rows.Scan(&o.ID, &o.UID, &o.Market, &side, &typ, &price, &qty, &filled, &status, &o.CreatedTs, &base, &quote); err != nil {
			return nil, err
		}
		o.Side, o.Type, o.Status = Side(side), OrderType(typ), OrderStatus(status)
		o.QuoteBase, o.QuoteQuote = Asset(base), Asset(quote)
		o.Price, o.Qty, o.Filled = parseAmt(price, o.QuoteQuote), parseAmt(qty, o.QuoteBase), parseAmt(filled, o.QuoteBase)
		o.PriceStr, o.QtyStr, o.FilledStr = o.Price.String(o.QuoteQuote), o.Qty.String(o.QuoteBase), o.Filled.String(o.QuoteBase)
		o.RemainingStr = o.remaining().String(o.QuoteBase)
		out = append(out, o)
	}
	return out, rows.Err()
}

// insertTrade 寫成交紀錄。
type Trade struct {
	ID           int64  `json:"id"`
	Market       string `json:"market"`
	Ts           int64  `json:"ts"`
	TakerSide    Side   `json:"taker_side"`
	Price        Amount `json:"-"`
	Qty          Amount `json:"-"`
	Quote        Amount `json:"-"`
	TakerFee     Amount `json:"-"`
	MakerFee     Amount `json:"-"`
	TakerUID     string `json:"taker_uid"`
	MakerUID     string `json:"maker_uid"`
	TakerOrderID int64  `json:"taker_order_id"`
	MakerOrderID int64  `json:"maker_order_id"`
	PriceStr     string `json:"price"`
	QtyStr       string `json:"qty"`
	QuoteStr     string `json:"quote"`
}

func (s *Store) insertTrade(t *Trade) error {
	if _, err := s.db.Exec(
		`INSERT INTO ex_trades (market, ts, taker_side, price, qty, quote, taker_fee, maker_fee,
		 taker_uid, maker_uid, taker_order_id, maker_order_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.Market, t.Ts, string(t.TakerSide),
		t.Price.ToBig().String(), t.Qty.ToBig().String(), t.Quote.ToBig().String(),
		t.TakerFee.ToBig().String(), t.MakerFee.ToBig().String(),
		t.TakerUID, t.MakerUID, t.TakerOrderID, t.MakerOrderID); err != nil {
		return fmt.Errorf("exchange: 寫成交: %w", err)
	}
	return nil
}

// Trades 讀取某交易對最近成交。
func (s *Store) Trades(market string, limit int) ([]*Trade, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT id, market, ts, taker_side, price, qty, quote, taker_fee, maker_fee,
		 taker_uid, maker_uid, taker_order_id, maker_order_id
		 FROM ex_trades WHERE market = ? ORDER BY id DESC LIMIT ?`, market, limit)
	if err != nil {
		return nil, fmt.Errorf("exchange: 讀成交: %w", err)
	}
	defer rows.Close()
	out := []*Trade{}
	for rows.Next() {
		t := &Trade{}
		var side, price, qty, quote, tf, mf string
		if err := rows.Scan(&t.ID, &t.Market, &t.Ts, &side, &price, &qty, &quote, &tf, &mf,
			&t.TakerUID, &t.MakerUID, &t.TakerOrderID, &t.MakerOrderID); err != nil {
			return nil, err
		}
		t.TakerSide = Side(side)
		qa, ba := quoteAssetOf(t.Market), baseAssetOf(t.Market)
		t.Price, t.Qty, t.Quote = parseAmt(price, qa), parseAmt(qty, ba), parseAmt(quote, qa)
		t.TakerFee, t.MakerFee = parseAmt(tf, qa), parseAmt(mf, qa)
		t.PriceStr, t.QtyStr, t.QuoteStr = t.Price.String(qa), t.Qty.String(ba), t.Quote.String(qa)
		out = append(out, t)
	}
	return out, rows.Err()
}

func parseAmt(s string, a Asset) Amount {
	if a == AssetTACm {
		return Amount{Big: mustBig(s)}
	}
	return Amount{I64: mustI64(s)}
}

func mustBig(s string) *big.Int {
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return new(big.Int)
	}
	return v
}

func mustI64(s string) int64 {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func baseAssetOf(market string) Asset {
	m, err := ParseMarket(market)
	if err != nil {
		return AssetTACm
	}
	return m.Base
}

func quoteAssetOf(market string) Asset {
	m, err := ParseMarket(market)
	if err != nil {
		return AssetUSDT
	}
	return m.Quote
} // ordersByUID 依用戶（可選市場）讀取訂單（新→舊）。
func (s *Store) ordersByUID(uid, market string) ([]*Order, error) {
	var rows *sql.Rows
	var err error
	if market != "" {
		rows, err = s.db.Query(`SELECT id, uid, market, side, type, price, qty, filled, status, created_ts, base, quote
			FROM ex_orders WHERE uid = ? AND market = ? ORDER BY id DESC LIMIT 200`, uid, market)
	} else {
		rows, err = s.db.Query(`SELECT id, uid, market, side, type, price, qty, filled, status, created_ts, base, quote
			FROM ex_orders WHERE uid = ? ORDER BY id DESC LIMIT 200`, uid)
	}
	if err != nil {
		return nil, fmt.Errorf("exchange: 查詢訂單: %w", err)
	}
	defer rows.Close()
	var out []*Order
	for rows.Next() {
		o := &Order{}
		var priceS, qtyS, filledS, base, quote string
		if err := rows.Scan(&o.ID, &o.UID, &o.Market, &o.Side, &o.Type, &priceS, &qtyS, &filledS, &o.Status, &o.CreatedTs, &base, &quote); err != nil {
			return nil, fmt.Errorf("exchange: 讀訂單: %w", err)
		}
		o.QuoteBase, o.QuoteQuote = Asset(base), Asset(quote)
		o.Price, _ = NewAmount(priceS, o.QuoteQuote)
		o.Qty, _ = NewAmount(qtyS, o.QuoteBase)
		o.Filled, _ = NewAmount(filledS, o.QuoteBase)
		out = append(out, o)
	}
	return out, rows.Err()
}

// ---------- M101：K 線與個人成交 ----------

// Kline 一根 K 線（開高低收＋成交量，顯示層 float）。
type Kline struct {
	Ts     int64   `json:"ts"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume float64 `json:"volume"`
}

// Klines 按 interval 聚合 ex_trades 為 OHLC K 線（時間升序，最多 limit 根）。
func (s *Store) Klines(market string, intervalSec, limit int) ([]Kline, error) {
	if intervalSec <= 0 {
		intervalSec = 60
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	m, err := ParseMarket(market)
	if err != nil {
		return nil, fmt.Errorf("exchange: 市場無效: %w", err)
	}
	priceDiv := math.Pow10(m.Quote.Decimals())
	qtyDiv := math.Pow10(m.Base.Decimals())
	rows, err := s.db.Query(
		`SELECT ts, taker_side, price, qty FROM ex_trades WHERE market=? ORDER BY id DESC LIMIT ?`,
		market, limit*50)
	if err != nil {
		return nil, fmt.Errorf("exchange: K 線讀取: %w", err)
	}
	defer rows.Close()
	type agg struct{ o, h, l, c, v float64 }
	buckets := map[int64]*agg{}
	var keys []int64
	for rows.Next() {
		var ts int64
		var side, price, qty string
		if err := rows.Scan(&ts, &side, &price, &qty); err != nil {
			return nil, fmt.Errorf("exchange: K 線掃描: %w", err)
		}
		bt := ts - ts%int64(intervalSec)
		p, err1 := strconv.ParseFloat(price, 64)
		q, err2 := strconv.ParseFloat(qty, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		p /= priceDiv
		q /= qtyDiv
		a, ok := buckets[bt]
		if !ok {
			a = &agg{o: p, h: p, l: p, c: p}
			buckets[bt] = a
			keys = append(keys, bt)
		}
		if p > a.h {
			a.h = p
		}
		if p < a.l {
			a.l = p
		}
		a.c = p
		a.v += q
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	out := make([]Kline, 0, len(keys))
	for _, k := range keys {
		a := buckets[k]
		out = append(out, Kline{Ts: k, Open: a.o, High: a.h, Low: a.l, Close: a.c, Volume: a.v})
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

// MyTrades 個人成交紀錄（作為 taker 或 maker，時間降序）。
func (s *Store) MyTrades(uid, market string, limit int) ([]Trade, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT id, market, ts, taker_side, price, qty, quote, taker_fee, maker_fee,
		taker_uid, maker_uid, taker_order_id, maker_order_id FROM ex_trades
		WHERE (taker_uid=? OR maker_uid=?)`
	args := []any{uid, uid}
	if market != "" {
		m, err := ParseMarket(market)
		if err != nil {
			return nil, fmt.Errorf("exchange: 市場無效: %w", err)
		}
		q += ` AND market=?`
		args = append(args, m.String())
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("exchange: 查詢成交: %w", err)
	}
	defer rows.Close()
	var out []Trade
	for rows.Next() {
		var t Trade
		var side, priceS, qtyS, quoteS, tfee, mfee string
		if err := rows.Scan(&t.ID, &t.Market, &t.Ts, &side, &priceS, &qtyS, &quoteS, &tfee, &mfee,
			&t.TakerUID, &t.MakerUID, &t.TakerOrderID, &t.MakerOrderID); err != nil {
			return nil, fmt.Errorf("exchange: 讀成交: %w", err)
		}
		t.TakerSide = Side(side)
		m, err := ParseMarket(t.Market)
		if err != nil {
			continue
		}
		t.Price, _ = NewAmount(priceS, m.Quote)
		t.Qty, _ = NewAmount(qtyS, m.Base)
		t.Quote, _ = NewAmount(quoteS, m.Quote)
		t.TakerFee, _ = NewAmount(tfee, m.Quote)
		t.MakerFee, _ = NewAmount(mfee, m.Quote)
		t.PriceStr = priceS
		t.QtyStr = qtyS
		t.QuoteStr = quoteS
		out = append(out, t)
	}
	return out, rows.Err()
}

// RecentTrades 最近成交（市場必填，全域最新成交，供門戶/行情使用）。
func (s *Store) RecentTrades(market string, limit int) ([]Trade, error) {
	if limit <= 0 || limit > 50 {
		limit = 8
	}
	q := `SELECT id, market, ts, taker_side, price, qty, quote, taker_fee, maker_fee,
		taker_uid, maker_uid, taker_order_id, maker_order_id FROM ex_trades
		WHERE market=? ORDER BY id DESC LIMIT ?`
	rows, err := s.db.Query(q, market, limit)
	if err != nil {
		return nil, fmt.Errorf("exchange: 查詢最近成交: %w", err)
	}
	defer rows.Close()
	var out []Trade
	for rows.Next() {
		var t Trade
		var side, priceS, qtyS, quoteS, tfee, mfee string
		if err := rows.Scan(&t.ID, &t.Market, &t.Ts, &side, &priceS, &qtyS, &quoteS, &tfee, &mfee,
			&t.TakerUID, &t.MakerUID, &t.TakerOrderID, &t.MakerOrderID); err != nil {
			return nil, fmt.Errorf("exchange: 讀最近成交: %w", err)
		}
		t.TakerSide = Side(side)
		m, err := ParseMarket(t.Market)
		if err != nil {
			continue
		}
		t.Price, _ = NewAmount(priceS, m.Quote)
		t.Qty, _ = NewAmount(qtyS, m.Base)
		t.Quote, _ = NewAmount(quoteS, m.Quote)
		t.TakerFee, _ = NewAmount(tfee, m.Quote)
		t.MakerFee, _ = NewAmount(mfee, m.Quote)
		t.PriceStr = priceS
		t.QtyStr = qtyS
		t.QuoteStr = quoteS
		out = append(out, t)
	}
	return out, rows.Err()
}
