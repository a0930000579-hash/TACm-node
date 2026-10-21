// Package c2c 場外交易（法幣對加密貨幣，照 Python c2c_system.py 搬運）。
// 設計差異（Go 慣用＋真實資產）：user_id→錢包地址；賣出廣告下單即鏈上凍結
// （wallet.Transfer 入 escrow 帳戶），放行/取消由 escrow 內部移轉（免二次費）；
// 買入廣告放行時由廣告主（訂單賣方）向買家支付（Transfer）。
package c2c

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 支援的法幣與支付方式（照 Python）。
var FiatCurrencies = []string{"TWD", "CNY", "USD", "HKD", "JPY"}
var PaymentMethods = []string{"銀行轉帳", "支付寶", "微信支付", "PayPal", "LINE Pay", "街口支付"}
var CryptoAssets = []string{"USDT", "TiUSD", "TACm"}

// EscrowAccount C2C 擔保帳戶名。
const EscrowAccount = "c2c_escrow"

// 訂單狀態（照 Python）。
const (
	StatusPending   = "pending"   // 待付款
	StatusPaid      = "paid"      // 待放行
	StatusCompleted = "completed" // 已完成
	StatusCancelled = "cancelled" // 已取消
	StatusDisputed  = "disputed"  // 申訴中
)

// Ad C2C 廣告。
type Ad struct {
	ID              int64    `json:"id"`
	Address         string   `json:"address"`
	Side            string   `json:"side"` // buy / sell
	Asset           string   `json:"asset"`
	Fiat            string   `json:"fiat"`
	Price           float64  `json:"price"`
	MinAmount       float64  `json:"min_amount"`
	MaxAmount       float64  `json:"max_amount"`
	PaymentMethods  []string `json:"payment_methods"`
	Status          string   `json:"status"`
	CreatedAt       int64    `json:"created_at"`
	CompletedOrders int64    `json:"completed_orders"`
	Rating          float64  `json:"rating"`
}

// Order C2C 訂單。
type Order struct {
	ID               int64   `json:"id"`
	OrderNo          string  `json:"order_no"`
	AdID             int64   `json:"ad_id"`
	Buyer            string  `json:"buyer"`
	Seller           string  `json:"seller"`
	Asset            string  `json:"asset"`
	Fiat             string  `json:"fiat"`
	Price            float64 `json:"price"`
	Amount           float64 `json:"amount"`
	TotalFiat        float64 `json:"total_fiat"`
	PaymentMethod    string  `json:"payment_method"`
	Status           string  `json:"status"`
	BuyerPaidAt      int64   `json:"buyer_paid_at"`
	SellerReleasedAt int64   `json:"seller_released_at"`
	CreatedAt        int64   `json:"created_at"`
	DisputeReason    string  `json:"dispute_reason"`
	DisputeAt        int64   `json:"dispute_at"`
}

// Store C2C 資料庫（SQLite）。
type Store struct {
	db *sql.DB
}

// Open 開啟 C2C 資料庫（自動建表）。
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("c2c: 建立資料目錄: %w", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "c2c.db"))
	if err != nil {
		return nil, fmt.Errorf("c2c: 開啟資料庫: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS c2c_ads (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		address TEXT NOT NULL,
		side TEXT NOT NULL,
		asset TEXT NOT NULL,
		fiat TEXT NOT NULL,
		price REAL NOT NULL,
		min_amount REAL NOT NULL,
		max_amount REAL NOT NULL,
		payment_methods TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'active',
		created_at INTEGER NOT NULL,
		completed_orders INTEGER DEFAULT 0,
		rating REAL DEFAULT 5.0
	);
	CREATE INDEX IF NOT EXISTS idx_c2c_ads_lookup ON c2c_ads(asset, fiat, status);
	CREATE TABLE IF NOT EXISTS c2c_orders (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		order_no TEXT UNIQUE NOT NULL,
		ad_id INTEGER NOT NULL,
		buyer TEXT NOT NULL,
		seller TEXT NOT NULL,
		asset TEXT NOT NULL,
		fiat TEXT NOT NULL,
		price REAL NOT NULL,
		amount REAL NOT NULL,
		total_fiat REAL NOT NULL,
		payment_method TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending',
		buyer_paid_at INTEGER DEFAULT 0,
		seller_released_at INTEGER DEFAULT 0,
		created_at INTEGER NOT NULL,
		dispute_reason TEXT,
		dispute_at INTEGER DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_c2c_orders_buyer ON c2c_orders(buyer);
	CREATE INDEX IF NOT EXISTS idx_c2c_orders_seller ON c2c_orders(seller);`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("c2c: 建表: %w", err)
	}
	return &Store{db: db}, nil
}

// Close 關閉資料庫。
func (s *Store) Close() error { return s.db.Close() }

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// CreateAd 創建廣告（照 Python 驗證）。
func (s *Store) CreateAd(address, side, asset, fiat string, price, minAmt, maxAmt float64, payMethods []string) (int64, error) {
	address = strings.TrimSpace(address)
	if side != "buy" && side != "sell" {
		return 0, fmt.Errorf("廣告類型必須是 buy 或 sell")
	}
	if !contains(CryptoAssets, asset) {
		return 0, fmt.Errorf("不支援的資產: %s", asset)
	}
	if !contains(FiatCurrencies, fiat) {
		return 0, fmt.Errorf("不支援的法幣: %s", fiat)
	}
	if price <= 0 {
		return 0, fmt.Errorf("價格必須大於 0")
	}
	if minAmt <= 0 || maxAmt <= 0 || minAmt > maxAmt {
		return 0, fmt.Errorf("金額範圍無效")
	}
	if len(payMethods) == 0 {
		return 0, fmt.Errorf("至少選擇一種支付方式")
	}
	for _, m := range payMethods {
		if !contains(PaymentMethods, m) {
			return 0, fmt.Errorf("不支援的支付方式: %s", m)
		}
	}
	b, _ := json.Marshal(payMethods)
	res, err := s.db.Exec(`INSERT INTO c2c_ads(address, side, asset, fiat, price, min_amount, max_amount, payment_methods, status, created_at)
		VALUES(?,?,?,?,?,?,?,?,'active',?)`, address, side, asset, fiat, price, minAmt, maxAmt, string(b), time.Now().Unix())
	if err != nil {
		return 0, fmt.Errorf("c2c: 建立廣告: %w", err)
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// ListAds 列出有效廣告（價格升序）。
func (s *Store) ListAds(asset, fiat, side string, limit int) ([]Ad, error) {
	query := `SELECT id, address, side, asset, fiat, price, min_amount, max_amount, payment_methods, status, created_at, completed_orders, rating
		FROM c2c_ads WHERE asset=? AND fiat=? AND status='active'`
	args := []any{asset, fiat}
	if side != "" {
		query += " AND side=?"
		args = append(args, side)
	}
	query += " ORDER BY price ASC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("c2c: 列出廣告: %w", err)
	}
	defer rows.Close()
	out := []Ad{}
	for rows.Next() {
		var a Ad
		var methods string
		if err := rows.Scan(&a.ID, &a.Address, &a.Side, &a.Asset, &a.Fiat, &a.Price, &a.MinAmount, &a.MaxAmount,
			&methods, &a.Status, &a.CreatedAt, &a.CompletedOrders, &a.Rating); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(methods), &a.PaymentMethods)
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetAd 單筆廣告。
func (s *Store) GetAd(id int64) (*Ad, error) {
	row := s.db.QueryRow(`SELECT id, address, side, asset, fiat, price, min_amount, max_amount, payment_methods, status, created_at, completed_orders, rating FROM c2c_ads WHERE id=?`, id)
	var a Ad
	var methods string
	if err := row.Scan(&a.ID, &a.Address, &a.Side, &a.Asset, &a.Fiat, &a.Price, &a.MinAmount, &a.MaxAmount,
		&methods, &a.Status, &a.CreatedAt, &a.CompletedOrders, &a.Rating); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("c2c: 查詢廣告: %w", err)
	}
	_ = json.Unmarshal([]byte(methods), &a.PaymentMethods)
	return &a, nil
}

// MyAds 用戶的廣告。
func (s *Store) MyAds(address string) ([]Ad, error) {
	rows, err := s.db.Query(`SELECT id, address, side, asset, fiat, price, min_amount, max_amount, payment_methods, status, created_at, completed_orders, rating
		FROM c2c_ads WHERE address=? ORDER BY created_at DESC`, address)
	if err != nil {
		return nil, fmt.Errorf("c2c: 讀取廣告: %w", err)
	}
	defer rows.Close()
	out := []Ad{}
	for rows.Next() {
		var a Ad
		var methods string
		if err := rows.Scan(&a.ID, &a.Address, &a.Side, &a.Asset, &a.Fiat, &a.Price, &a.MinAmount, &a.MaxAmount,
			&methods, &a.Status, &a.CreatedAt, &a.CompletedOrders, &a.Rating); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(methods), &a.PaymentMethods)
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpdateAdStatus 更新廣告狀態（僅廣告主）。
func (s *Store) UpdateAdStatus(adID int64, address, status string) error {
	if status != "active" && status != "paused" && status != "closed" {
		return fmt.Errorf("狀態無效")
	}
	var cnt int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM c2c_ads WHERE id=? AND address=?`, adID, address).Scan(&cnt); err != nil {
		return fmt.Errorf("c2c: 查詢廣告: %w", err)
	}
	if cnt == 0 {
		return fmt.Errorf("廣告不存在或無權操作")
	}
	if _, err := s.db.Exec(`UPDATE c2c_ads SET status=? WHERE id=?`, status, adID); err != nil {
		return fmt.Errorf("c2c: 更新廣告: %w", err)
	}
	return nil
}

// CreateOrder 創建訂單（買家下單；sell 廣告由外部執行鏈上凍結後呼叫）。
func (s *Store) CreateOrder(adID int64, buyer, payMethod string, amount float64) (*Order, error) {
	ad, err := s.GetAd(adID)
	if err != nil {
		return nil, err
	}
	if ad == nil {
		return nil, fmt.Errorf("廣告不存在")
	}
	if ad.Status != "active" {
		return nil, fmt.Errorf("廣告已下架")
	}
	if amount < ad.MinAmount || amount > ad.MaxAmount {
		return nil, fmt.Errorf("數量必須在 %.2f - %.2f 之間", ad.MinAmount, ad.MaxAmount)
	}
	if buyer == ad.Address {
		return nil, fmt.Errorf("不能與自己交易")
	}
	if !contains(PaymentMethods, payMethod) {
		return nil, fmt.Errorf("不支援的支付方式: %s", payMethod)
	}
	totalFiat := round2(amount * ad.Price)
	orderNo := fmt.Sprintf("C2C%08x%08x", time.Now().Unix(), time.Now().UnixNano()&0xffffffff)
	res, err := s.db.Exec(`INSERT INTO c2c_orders(order_no, ad_id, buyer, seller, asset, fiat, price, amount, total_fiat, payment_method, status, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,'pending',?)`, orderNo, adID, buyer, ad.Address, ad.Asset, ad.Fiat, ad.Price, amount, totalFiat, payMethod, time.Now().Unix())
	if err != nil {
		return nil, fmt.Errorf("c2c: 建立訂單: %w", err)
	}
	id, _ := res.LastInsertId()
	return &Order{ID: id, OrderNo: orderNo, AdID: adID, Buyer: buyer, Seller: ad.Address, Asset: ad.Asset,
		Fiat: ad.Fiat, Price: ad.Price, Amount: amount, TotalFiat: totalFiat, PaymentMethod: payMethod, Status: StatusPending}, nil
}

// GetOrder 單筆訂單。
func (s *Store) GetOrder(id int64) (*Order, error) {
	row := s.db.QueryRow(`SELECT id, order_no, ad_id, buyer, seller, asset, fiat, price, amount, total_fiat, payment_method, status, buyer_paid_at, seller_released_at, created_at, COALESCE(dispute_reason,''), dispute_at FROM c2c_orders WHERE id=?`, id)
	var o Order
	if err := row.Scan(&o.ID, &o.OrderNo, &o.AdID, &o.Buyer, &o.Seller, &o.Asset, &o.Fiat, &o.Price, &o.Amount,
		&o.TotalFiat, &o.PaymentMethod, &o.Status, &o.BuyerPaidAt, &o.SellerReleasedAt, &o.CreatedAt, &o.DisputeReason, &o.DisputeAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("c2c: 查詢訂單: %w", err)
	}
	return &o, nil
}

// ConfirmPayment 買家確認已付款：pending→paid。
func (s *Store) ConfirmPayment(orderID int64, address string) error {
	o, err := s.GetOrder(orderID)
	if err != nil {
		return err
	}
	if o == nil {
		return fmt.Errorf("訂單不存在")
	}
	if o.Buyer != address {
		return fmt.Errorf("只有買家可以確認付款")
	}
	if o.Status != StatusPending {
		return fmt.Errorf("訂單狀態異常")
	}
	if _, err := s.db.Exec(`UPDATE c2c_orders SET status='paid', buyer_paid_at=? WHERE id=?`, time.Now().Unix(), orderID); err != nil {
		return fmt.Errorf("c2c: 確認付款: %w", err)
	}
	return nil
}

// Release 賣家放行：paid→completed，並更新廣告完成數。
func (s *Store) Release(orderID int64, address string) error {
	o, err := s.GetOrder(orderID)
	if err != nil {
		return err
	}
	if o == nil {
		return fmt.Errorf("訂單不存在")
	}
	if o.Seller != address {
		return fmt.Errorf("只有賣家可以放行")
	}
	if o.Status != StatusPaid {
		return fmt.Errorf("買家尚未確認付款")
	}
	if _, err := s.db.Exec(`UPDATE c2c_orders SET status='completed', seller_released_at=? WHERE id=?`, time.Now().Unix(), orderID); err != nil {
		return fmt.Errorf("c2c: 放行: %w", err)
	}
	if _, err := s.db.Exec(`UPDATE c2c_ads SET completed_orders=completed_orders+1 WHERE id=?`, o.AdID); err != nil {
		return fmt.Errorf("c2c: 更新廣告完成數: %w", err)
	}
	return nil
}

// CancelOrder 取消訂單（僅 pending）。
func (s *Store) CancelOrder(orderID int64, address string) error {
	o, err := s.GetOrder(orderID)
	if err != nil {
		return err
	}
	if o == nil {
		return fmt.Errorf("訂單不存在")
	}
	if o.Buyer != address && o.Seller != address {
		return fmt.Errorf("無權操作此訂單")
	}
	if o.Status != StatusPending {
		return fmt.Errorf("當前狀態不能取消")
	}
	if _, err := s.db.Exec(`UPDATE c2c_orders SET status='cancelled' WHERE id=?`, orderID); err != nil {
		return fmt.Errorf("c2c: 取消訂單: %w", err)
	}
	return nil
}

// DisputeOrder 申訴（pending/paid→disputed）。
func (s *Store) DisputeOrder(orderID int64, address, reason string) error {
	o, err := s.GetOrder(orderID)
	if err != nil {
		return err
	}
	if o == nil {
		return fmt.Errorf("訂單不存在")
	}
	if o.Buyer != address && o.Seller != address {
		return fmt.Errorf("無權操作此訂單")
	}
	if o.Status != StatusPending && o.Status != StatusPaid {
		return fmt.Errorf("當前狀態不能申訴")
	}
	if _, err := s.db.Exec(`UPDATE c2c_orders SET status='disputed', dispute_reason=?, dispute_at=? WHERE id=?`,
		reason, time.Now().Unix(), orderID); err != nil {
		return fmt.Errorf("c2c: 申訴: %w", err)
	}
	return nil
}

// MyOrders 用戶訂單（買或賣方）。
func (s *Store) MyOrders(address string, limit int) ([]Order, error) {
	rows, err := s.db.Query(`SELECT id, order_no, ad_id, buyer, seller, asset, fiat, price, amount, total_fiat, payment_method, status, buyer_paid_at, seller_released_at, created_at, COALESCE(dispute_reason,''), dispute_at
		FROM c2c_orders WHERE buyer=? OR seller=? ORDER BY created_at DESC LIMIT ?`, address, address, limit)
	if err != nil {
		return nil, fmt.Errorf("c2c: 讀取訂單: %w", err)
	}
	defer rows.Close()
	out := []Order{}
	for rows.Next() {
		var o Order
		if err := rows.Scan(&o.ID, &o.OrderNo, &o.AdID, &o.Buyer, &o.Seller, &o.Asset, &o.Fiat, &o.Price, &o.Amount,
			&o.TotalFiat, &o.PaymentMethod, &o.Status, &o.BuyerPaidAt, &o.SellerReleasedAt, &o.CreatedAt, &o.DisputeReason, &o.DisputeAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}
