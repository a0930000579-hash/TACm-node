package aimarket

// aimarket.go — TAC 鏈上 AI 模型市場（對標 Bittensor / Ritual，簡化版）：
// 模型註冊／列表搜尋／訂閱／呼叫記錄／評分。
// 本包只維護純狀態與 SQLite 持久化（aimarket.db）；實際 TACm/TiUSD 資金移轉
// 由 node handler 透過 wallet 服務完成（對標 internal/ens 的分工）。

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// 計費方式。
const (
	// PriceTypeCall 按次計費：每次呼叫當次付費，無需預先訂閱。
	PriceTypeCall = "per_call"
	// PriceTypePeriod 按期計費：需先訂閱付費，訂閱期內可呼叫。
	PriceTypePeriod = "per_period"
)

// 預設參數。
const (
	// FeeAccount 平台抽成入帳帳戶。
	FeeAccount = "aimarket_fee"
	// ProtocolFeeRate 平台抽成比例（price 的 10% 進 FeeAccount，其餘給 owner）。
	ProtocolFeeRate = 0.10
	// DefaultPeriodSec per_period 預設期長（30 天）。
	DefaultPeriodSec int64 = 30 * 24 * 3600
)

// Model 鏈上 AI 模型。
type Model struct {
	Id          int64   `json:"id"`
	Owner       string  `json:"owner"`
	Name        string  `json:"name"`
	Metadata    string  `json:"metadata"`
	CID         string  `json:"cid"`
	URL         string  `json:"url"`
	Version     string  `json:"version"`
	Price       float64 `json:"price"`
	PriceType   string  `json:"price_type"`
	PeriodSec   int64   `json:"period_sec"`
	AvgRating   float64 `json:"avg_rating"`
	RatingCount int64   `json:"rating_count"`
	InvokeCount int64   `json:"invoke_count"`
	SubCount    int64   `json:"sub_count"`
	CreatedAt   int64   `json:"created_at"`
}

// Subscription 訂閱記錄（per_period 模型）。
type Subscription struct {
	Id         int64  `json:"id"`
	Subscriber string `json:"subscriber"`
	ModelId    int64  `json:"model_id"`
	ExpiresAt  int64  `json:"expires_at"`
	Active     bool   `json:"active"`
	CreatedAt  int64  `json:"created_at"`
}

// Invocation 一次呼叫記錄。
type Invocation struct {
	Id        int64   `json:"id"`
	Caller    string  `json:"caller"`
	ModelId   int64   `json:"model_id"`
	Fee       float64 `json:"fee"`
	Status    string  `json:"status"`
	CreatedAt int64   `json:"created_at"`
}

// Rating 評分記錄（一個 rater 對一個 model 一條，可更新）。
type Rating struct {
	ModelId   int64  `json:"model_id"`
	Rater     string `json:"rater"`
	Score     int64  `json:"score"`
	Comment   string `json:"comment"`
	CreatedAt int64  `json:"created_at"`
}

// Store AI 模型市場持久化。
type Store struct {
	db *sql.DB
	mu sync.Mutex
}

// Open 開啟 aimarket.db（WAL、單連線）並建表。
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("aimarket: 建立數據目錄: %w", err)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)",
		filepath.Join(dataDir, "aimarket.db"))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("aimarket: 開啟數據庫: %w", err)
	}
	db.SetMaxOpenConns(1)
	schema := `
CREATE TABLE IF NOT EXISTS aim_models (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  owner TEXT NOT NULL,
  name TEXT NOT NULL,
  metadata TEXT NOT NULL DEFAULT '',
  cid TEXT NOT NULL DEFAULT '',
  url TEXT NOT NULL DEFAULT '',
  version TEXT NOT NULL DEFAULT '1',
  price REAL NOT NULL DEFAULT 0,
  price_type TEXT NOT NULL DEFAULT 'per_call',
  period_sec INTEGER NOT NULL DEFAULT 0,
  avg_rating REAL NOT NULL DEFAULT 0,
  rating_count INTEGER NOT NULL DEFAULT 0,
  invoke_count INTEGER NOT NULL DEFAULT 0,
  sub_count INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_aim_models_owner ON aim_models(owner);
CREATE INDEX IF NOT EXISTS idx_aim_models_name ON aim_models(name);

CREATE TABLE IF NOT EXISTS aim_subscriptions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  subscriber TEXT NOT NULL,
  model_id INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  active INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_aim_sub_subscriber ON aim_subscriptions(subscriber);
CREATE INDEX IF NOT EXISTS idx_aim_sub_model ON aim_subscriptions(model_id);

CREATE TABLE IF NOT EXISTS aim_invocations (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  caller TEXT NOT NULL,
  model_id INTEGER NOT NULL,
  fee REAL NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'ok',
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_aim_inv_caller ON aim_invocations(caller);
CREATE INDEX IF NOT EXISTS idx_aim_inv_model ON aim_invocations(model_id);

CREATE TABLE IF NOT EXISTS aim_ratings (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  model_id INTEGER NOT NULL,
  rater TEXT NOT NULL,
  score INTEGER NOT NULL,
  comment TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  UNIQUE(model_id, rater)
);
CREATE INDEX IF NOT EXISTS idx_aim_ratings_model ON aim_ratings(model_id);
`
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("aimarket: 初始化 schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close 關閉資料庫。
func (s *Store) Close() error { return s.db.Close() }

// isValidPriceType 校驗計費方式。
func isValidPriceType(pt string) bool {
	return pt == PriceTypeCall || pt == PriceTypePeriod
}

// nextVersion 依同 owner+name 既有版本推進下一個版本號（數字遞增）。
func (s *Store) nextVersion(owner, name string, hint string) (string, error) {
	rows, err := s.db.Query(`SELECT version FROM aim_models WHERE owner=? AND name=? ORDER BY id ASC`, owner, name)
	if err != nil {
		return "", fmt.Errorf("aimarket: 查詢既有版本: %w", err)
	}
	defer rows.Close()
	maxVer := int64(0)
	found := false
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return "", err
		}
		if n, perr := strconv.ParseInt(strings.TrimSpace(v), 10, 64); perr == nil {
			if n > maxVer {
				maxVer = n
			}
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if !found {
		hint = strings.TrimSpace(hint)
		if hint == "" {
			hint = "1"
		}
		if n, perr := strconv.ParseInt(hint, 10, 64); perr == nil && n > 0 {
			return strconv.FormatInt(n, 10), nil
		}
		return hint, nil
	}
	return strconv.FormatInt(maxVer+1, 10), nil
}

// RegisterModel 註冊模型（同名同 owner 自動遞增 version，保留舊版）。
// 付費（註冊本身不付費）由節點層處理；此處只寫記錄。回傳新建的模型。
func (s *Store) RegisterModel(owner, name, metadata, cid, url, version string,
	price float64, priceType string, periodSec int64) (*Model, error) {
	owner = strings.TrimSpace(owner)
	name = strings.TrimSpace(name)
	if owner == "" {
		return nil, fmt.Errorf("aimarket: 缺少 owner 地址")
	}
	if name == "" {
		return nil, fmt.Errorf("aimarket: 缺少模型名稱")
	}
	if strings.TrimSpace(cid) == "" && strings.TrimSpace(url) == "" {
		return nil, fmt.Errorf("aimarket: 至少需提供 CID 或 URL 其一")
	}
	if !isValidPriceType(priceType) {
		return nil, fmt.Errorf("aimarket: 非法計費方式 %q", priceType)
	}
	if price < 0 {
		return nil, fmt.Errorf("aimarket: 價格不可為負")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if priceType == PriceTypePeriod && periodSec <= 0 {
		periodSec = DefaultPeriodSec
	}
	ver, err := s.nextVersion(owner, name, version)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	res, err := s.db.Exec(`INSERT INTO aim_models
		(owner, name, metadata, cid, url, version, price, price_type, period_sec, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		owner, name, strings.TrimSpace(metadata), strings.TrimSpace(cid), strings.TrimSpace(url),
		ver, price, priceType, periodSec, now)
	if err != nil {
		return nil, fmt.Errorf("aimarket: 寫入模型: %w", err)
	}
	id, _ := res.LastInsertId()
	return s.getModelLocked(id)
}

// scanModel 從 rows 掃描一列 Model。
func scanModel(scanner interface{ Scan(...any) error }) (*Model, error) {
	var m Model
	err := scanner.Scan(&m.Id, &m.Owner, &m.Name, &m.Metadata, &m.CID, &m.URL, &m.Version,
		&m.Price, &m.PriceType, &m.PeriodSec, &m.AvgRating, &m.RatingCount,
		&m.InvokeCount, &m.SubCount, &m.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

const modelCols = `id, owner, name, metadata, cid, url, version, price, price_type, period_sec,
	avg_rating, rating_count, invoke_count, sub_count, created_at`

// getModelLocked 呼叫方須已持鎖。
func (s *Store) getModelLocked(id int64) (*Model, error) {
	row := s.db.QueryRow(`SELECT `+modelCols+` FROM aim_models WHERE id=?`, id)
	m, err := scanModel(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("aimarket: 模型不存在 (id=%d)", id)
	}
	if err != nil {
		return nil, fmt.Errorf("aimarket: 查詢模型: %w", err)
	}
	return m, nil
}

// GetModel 依 id 取模型。
func (s *Store) GetModel(id int64) (*Model, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getModelLocked(id)
}

// ListModels 列出模型。q 對 name/metadata 做 LIKE 搜尋；
// sort：popular（呼叫數）／rating（評分）／new（最新，預設）。
func (s *Store) ListModels(q, sort string) ([]Model, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var (
		rows *sql.Rows
		err  error
	)
	base := `SELECT ` + modelCols + ` FROM aim_models`
	order := " ORDER BY created_at DESC, id DESC"
	switch strings.ToLower(strings.TrimSpace(sort)) {
	case "popular":
		order = " ORDER BY invoke_count DESC, sub_count DESC, id DESC"
	case "rating":
		order = " ORDER BY avg_rating DESC, rating_count DESC, id DESC"
	case "new", "latest":
		order = " ORDER BY created_at DESC, id DESC"
	}
	if like := strings.TrimSpace(q); like != "" {
		pat := "%" + like + "%"
		rows, err = s.db.Query(base+` WHERE name LIKE ? OR metadata LIKE ?`+order+` LIMIT 200`, pat, pat)
	} else {
		rows, err = s.db.Query(base + order + ` LIMIT 200`)
	}
	if err != nil {
		return nil, fmt.Errorf("aimarket: 列表查詢: %w", err)
	}
	defer rows.Close()
	out := make([]Model, 0, 8)
	for rows.Next() {
		m, err := scanModel(rows)
		if err != nil {
			return nil, fmt.Errorf("aimarket: 掃描模型列: %w", err)
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// Subscribe 訂閱 per_period 模型（付費由節點層完成；此處寫訂閱記錄）。
// 既有未到期訂閱 → 在到期基礎上續期；否則從現在起算。
func (s *Store) Subscribe(subscriber string, modelID int64) (*Subscription, error) {
	subscriber = strings.TrimSpace(subscriber)
	if subscriber == "" {
		return nil, fmt.Errorf("aimarket: 缺少 subscriber 地址")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.getModelLocked(modelID)
	if err != nil {
		return nil, err
	}
	if m.PriceType != PriceTypePeriod {
		return nil, fmt.Errorf("aimarket: 模型 %d 為 per_call，無需訂閱", modelID)
	}
	now := time.Now().Unix()
	// 找最新一筆訂閱記錄。
	var (
		prevExp int64
		hasPrev bool
	)
	row := s.db.QueryRow(`SELECT expires_at FROM aim_subscriptions WHERE subscriber=? AND model_id=? ORDER BY id DESC LIMIT 1`,
		subscriber, modelID)
	if err := row.Scan(&prevExp); err == nil {
		hasPrev = true
	} else if err != sql.ErrNoRows {
		return nil, fmt.Errorf("aimarket: 查詢既有訂閱: %w", err)
	}
	base := now
	if hasPrev && prevExp > now {
		base = prevExp
	}
	expires := base + m.PeriodSec
	res, err := s.db.Exec(`INSERT INTO aim_subscriptions(subscriber, model_id, expires_at, active, created_at)
		VALUES(?,?,?,1,?)`, subscriber, modelID, expires, now)
	if err != nil {
		return nil, fmt.Errorf("aimarket: 寫入訂閱: %w", err)
	}
	id, _ := res.LastInsertId()
	if _, err := s.db.Exec(`UPDATE aim_models SET sub_count=sub_count+1 WHERE id=?`, modelID); err != nil {
		return nil, fmt.Errorf("aimarket: 更新訂閱數: %w", err)
	}
	return &Subscription{Id: id, Subscriber: subscriber, ModelId: modelID,
		ExpiresAt: expires, Active: true, CreatedAt: now}, nil
}

// HasActiveSubscription 判斷 subscriber 對 modelID 是否有有效（未過期）訂閱。
func (s *Store) HasActiveSubscription(subscriber string, modelID int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	var cnt int64
	err := s.db.QueryRow(`SELECT COUNT(1) FROM aim_subscriptions WHERE subscriber=? AND model_id=? AND expires_at>?`,
		strings.TrimSpace(subscriber), modelID, now).Scan(&cnt)
	if err != nil {
		return false, fmt.Errorf("aimarket: 檢查訂閱: %w", err)
	}
	return cnt > 0, nil
}

// GetSubscription 取 subscriber 對 modelID 的最新訂閱（active 依即時時間判定）。
func (s *Store) GetSubscription(subscriber string, modelID int64) (*Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	row := s.db.QueryRow(`SELECT id, subscriber, model_id, expires_at, created_at FROM aim_subscriptions
		WHERE subscriber=? AND model_id=? ORDER BY id DESC LIMIT 1`,
		strings.TrimSpace(subscriber), modelID)
	var sub Subscription
	if err := row.Scan(&sub.Id, &sub.Subscriber, &sub.ModelId, &sub.ExpiresAt, &sub.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("aimarket: 無訂閱記錄")
		}
		return nil, fmt.Errorf("aimarket: 查詢訂閱: %w", err)
	}
	sub.Active = sub.ExpiresAt > now
	return &sub, nil
}

// ListSubscriptions 列出某 subscriber 的全部訂閱（含模型名稱）。
func (s *Store) ListSubscriptions(subscriber string) ([]Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	rows, err := s.db.Query(`SELECT id, subscriber, model_id, expires_at, created_at FROM aim_subscriptions
		WHERE subscriber=? ORDER BY id DESC LIMIT 200`, strings.TrimSpace(subscriber))
	if err != nil {
		return nil, fmt.Errorf("aimarket: 查詢訂閱列表: %w", err)
	}
	defer rows.Close()
	out := make([]Subscription, 0, 4)
	for rows.Next() {
		var sub Subscription
		if err := rows.Scan(&sub.Id, &sub.Subscriber, &sub.ModelId, &sub.ExpiresAt, &sub.CreatedAt); err != nil {
			return nil, err
		}
		sub.Active = sub.ExpiresAt > now
		out = append(out, sub)
	}
	return out, rows.Err()
}

// RecordInvocation 記錄一次呼叫，並遞增模型 invoke_count。
func (s *Store) RecordInvocation(caller string, modelID int64, fee float64, status string) (*Invocation, error) {
	caller = strings.TrimSpace(caller)
	if caller == "" {
		return nil, fmt.Errorf("aimarket: 缺少 caller 地址")
	}
	if strings.TrimSpace(status) == "" {
		status = "ok"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.getModelLocked(modelID); err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	res, err := s.db.Exec(`INSERT INTO aim_invocations(caller, model_id, fee, status, created_at) VALUES(?,?,?,?,?)`,
		caller, modelID, fee, status, now)
	if err != nil {
		return nil, fmt.Errorf("aimarket: 寫入呼叫記錄: %w", err)
	}
	id, _ := res.LastInsertId()
	if _, err := s.db.Exec(`UPDATE aim_models SET invoke_count=invoke_count+1 WHERE id=?`, modelID); err != nil {
		return nil, fmt.Errorf("aimarket: 更新呼叫數: %w", err)
	}
	return &Invocation{Id: id, Caller: caller, ModelId: modelID, Fee: fee, Status: status, CreatedAt: now}, nil
}

// HasInvokedOrSubscribed 判斷 rater 是否有資格評分（曾呼叫過或訂閱過該模型）。
func (s *Store) HasInvokedOrSubscribed(rater string, modelID int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rater = strings.TrimSpace(rater)
	var cnt int64
	err := s.db.QueryRow(`SELECT
		(SELECT COUNT(1) FROM aim_invocations WHERE caller=? AND model_id=?) +
		(SELECT COUNT(1) FROM aim_subscriptions WHERE subscriber=? AND model_id=?)`,
		rater, modelID, rater, modelID).Scan(&cnt)
	if err != nil {
		return false, fmt.Errorf("aimarket: 評分資格檢查: %w", err)
	}
	return cnt > 0, nil
}

// RateModel 評分（同一 rater 對同一 model 重複評分＝更新），並重算模型平均分。
func (s *Store) RateModel(modelID int64, rater string, score int64, comment string) (*Rating, error) {
	rater = strings.TrimSpace(rater)
	if rater == "" {
		return nil, fmt.Errorf("aimarket: 缺少 rater 地址")
	}
	if score < 1 || score > 5 {
		return nil, fmt.Errorf("aimarket: 評分須介於 1–5")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.getModelLocked(modelID); err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	if _, err := s.db.Exec(`INSERT INTO aim_ratings(model_id, rater, score, comment, created_at)
		VALUES(?,?,?,?,?)
		ON CONFLICT(model_id, rater) DO UPDATE SET score=excluded.score, comment=excluded.comment, created_at=excluded.created_at`,
		modelID, rater, score, strings.TrimSpace(comment), now); err != nil {
		return nil, fmt.Errorf("aimarket: 寫入評分: %w", err)
	}
	if err := s.recomputeRatingLocked(modelID); err != nil {
		return nil, err
	}
	return &Rating{ModelId: modelID, Rater: rater, Score: score, Comment: strings.TrimSpace(comment), CreatedAt: now}, nil
}

// recomputeRatingLocked 重算模型平均評分與評分數（呼叫方須持鎖）。
func (s *Store) recomputeRatingLocked(modelID int64) error {
	var (
		sum   float64
		count int64
	)
	row := s.db.QueryRow(`SELECT COALESCE(AVG(score),0), COUNT(1) FROM aim_ratings WHERE model_id=?`, modelID)
	if err := row.Scan(&sum, &count); err != nil {
		return fmt.Errorf("aimarket: 聚合評分: %w", err)
	}
	if _, err := s.db.Exec(`UPDATE aim_models SET avg_rating=?, rating_count=? WHERE id=?`, sum, count, modelID); err != nil {
		return fmt.Errorf("aimarket: 更新評分統計: %w", err)
	}
	return nil
}

// ListRatings 列出某模型的評分。
func (s *Store) ListRatings(modelID int64, limit int64) ([]Rating, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT model_id, rater, score, comment, created_at FROM aim_ratings
		WHERE model_id=? ORDER BY created_at DESC LIMIT ?`, modelID, limit)
	if err != nil {
		return nil, fmt.Errorf("aimarket: 評分列表: %w", err)
	}
	defer rows.Close()
	out := make([]Rating, 0, 4)
	for rows.Next() {
		var r Rating
		if err := rows.Scan(&r.ModelId, &r.Rater, &r.Score, &r.Comment, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
