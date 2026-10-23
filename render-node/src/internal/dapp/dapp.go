// Package dapp 實作 TAC 鏈 DApp 商店（對標 Ethereum DApp 入口 / DApp 商店）：
// DApp 註冊、分類、搜尋、評分、收藏、熱門／精選推薦。
// 本包只維護純狀態與 SQLite 持久化（dapp.db）；錢包身分校驗、簽名交易等
// 一律由 node handler 層處理（對標 internal/aimarket 的分工）。
package dapp

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// 預設分類（首次建表後寫入；已存在則不重複）。
var defaultCategories = []string{
	"DeFi", "Exchange", "Game", "NFT", "Tools",
	"Social", "AI", "DAO", "Wallet", "Other",
}

// DApp 已註冊的鏈上 DApp。
type DApp struct {
	Id          int64   `json:"id"`
	Deployer    string  `json:"deployer"`
	Name        string  `json:"name"`
	Category    string  `json:"category"`
	Description string  `json:"description"`
	URL         string  `json:"url"`
	Icon        string  `json:"icon"`
	Contract    string  `json:"contract"`
	Tags        string  `json:"tags"`
	Version     int64   `json:"version"`
	RatingAvg   float64 `json:"rating_avg"`
	RatingCount int64   `json:"rating_count"`
	FavCount    int64   `json:"fav_count"`
	Views       int64   `json:"views"`
	Featured    bool    `json:"featured"`
	CreatedAt   int64   `json:"created_at"`
	UpdatedAt   int64   `json:"updated_at"`
}

// Rating 一筆評分（一個 address 對一個 dapp 一條，可更新）。
type Rating struct {
	DAppID    int64  `json:"dapp_id"`
	Address   string `json:"address"`
	Score     int64  `json:"score"`
	Comment   string `json:"comment"`
	CreatedAt int64  `json:"created_at"`
}

// Favorite 一筆收藏（一個 address 對一個 dapp 一條）。
type Favorite struct {
	DAppID    int64  `json:"dapp_id"`
	Address   string `json:"address"`
	CreatedAt int64  `json:"created_at"`
}

// ListParams 列表查詢參數。
type ListParams struct {
	Q          string // 名稱／描述／標籤關鍵字（LIKE）
	Category   string // 精確分類
	Sort       string // popular | rating | new | featured
	Page       int    // 1-based
	Size       int    // 每頁（預設 24，上限 100）
	Deployer   string // 若指定：只回該部署者（我的 DApp）
	ForAddress string // 若指定：該 address 的收藏狀態併回傳
}

// ListResult 列表回傳。
type ListResult struct {
	Total int64  `json:"total"`
	Page  int    `json:"page"`
	Size  int    `json:"size"`
	Items []DApp `json:"items"`
}

// Store DApp 商店持久化。
type Store struct {
	db *sql.DB
	mu sync.Mutex
}

// Open 開啟 dapp.db（WAL、單連線）並建表。
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("dapp: 建立數據目錄: %w", err)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)",
		filepath.Join(dataDir, "dapp.db"))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("dapp: 開啟數據庫: %w", err)
	}
	db.SetMaxOpenConns(1)
	schema := `
CREATE TABLE IF NOT EXISTS dapps (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  deployer TEXT NOT NULL,
  name TEXT NOT NULL,
  category TEXT NOT NULL DEFAULT 'Other',
  description TEXT NOT NULL DEFAULT '',
  url TEXT NOT NULL DEFAULT '',
  icon TEXT NOT NULL DEFAULT '',
  contract TEXT NOT NULL DEFAULT '',
  tags TEXT NOT NULL DEFAULT '',
  version INTEGER NOT NULL DEFAULT 1,
  rating_avg REAL NOT NULL DEFAULT 0,
  rating_count INTEGER NOT NULL DEFAULT 0,
  fav_count INTEGER NOT NULL DEFAULT 0,
  views INTEGER NOT NULL DEFAULT 0,
  featured INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE(deployer, name)
);
CREATE INDEX IF NOT EXISTS idx_dapps_deployer ON dapps(deployer);
CREATE INDEX IF NOT EXISTS idx_dapps_category ON dapps(category);
CREATE INDEX IF NOT EXISTS idx_dapps_name ON dapps(name);

CREATE TABLE IF NOT EXISTS dapp_ratings (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  dapp_id INTEGER NOT NULL,
  address TEXT NOT NULL,
  score INTEGER NOT NULL,
  comment TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  UNIQUE(dapp_id, address)
);
CREATE INDEX IF NOT EXISTS idx_ratings_dapp ON dapp_ratings(dapp_id);
CREATE INDEX IF NOT EXISTS idx_ratings_addr ON dapp_ratings(address);

CREATE TABLE IF NOT EXISTS dapp_favorites (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  dapp_id INTEGER NOT NULL,
  address TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  UNIQUE(dapp_id, address)
);
CREATE INDEX IF NOT EXISTS idx_fav_dapp ON dapp_favorites(dapp_id);
CREATE INDEX IF NOT EXISTS idx_fav_addr ON dapp_favorites(address);

CREATE TABLE IF NOT EXISTS dapp_categories (
  name TEXT PRIMARY KEY
);
`
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("dapp: 初始化 schema: %w", err)
	}
	s := &Store{db: db}
	if err := s.seedCategories(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close 關閉資料庫。
func (s *Store) Close() error { return s.db.Close() }

// seedCategories 寫入預設分類（已存在則忽略）。
func (s *Store) seedCategories() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range defaultCategories {
		if _, err := s.db.Exec(`INSERT OR IGNORE INTO dapp_categories(name) VALUES(?)`, c); err != nil {
			return fmt.Errorf("dapp: 寫入分類 %q: %w", c, err)
		}
	}
	return nil
}

// validURL 基本 URL 防呆：必須為 http/https 且可解析。
func validURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	u, err := url.ParseRequestURI(raw)
	if err != nil {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

const dappCols = `id, deployer, name, category, description, url, icon, contract, tags,
	version, rating_avg, rating_count, fav_count, views, featured, created_at, updated_at`

func scanDApp(sc interface{ Scan(...any) error }) (*DApp, error) {
	var d DApp
	var featured int
	if err := sc.Scan(&d.Id, &d.Deployer, &d.Name, &d.Category, &d.Description,
		&d.URL, &d.Icon, &d.Contract, &d.Tags, &d.Version, &d.RatingAvg, &d.RatingCount,
		&d.FavCount, &d.Views, &featured, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return nil, err
	}
	d.Featured = featured == 1
	return &d, nil
}

// Register 註冊／更新 DApp（同名同 deployer 視為更新，version 遞增）。
// deployer 必填且應為合法地址（合法性由呼叫端保證；此處只做非空）。
func (s *Store) Register(deployer, name, category, description, dappURL, icon, contract, tags string, featured bool) (*DApp, error) {
	deployer = strings.TrimSpace(deployer)
	name = strings.TrimSpace(name)
	category = strings.TrimSpace(category)
	description = strings.TrimSpace(description)
	dappURL = strings.TrimSpace(dappURL)
	icon = strings.TrimSpace(icon)
	contract = strings.TrimSpace(contract)
	tags = strings.TrimSpace(tags)

	if deployer == "" {
		return nil, fmt.Errorf("dapp: 缺少 deployer 地址")
	}
	if name == "" {
		return nil, fmt.Errorf("dapp: 缺少名稱")
	}
	if len(name) > 64 {
		return nil, fmt.Errorf("dapp: 名稱過長（上限 64）")
	}
	if len(description) > 1000 {
		return nil, fmt.Errorf("dapp: 描述過長（上限 1000）")
	}
	if !validURL(dappURL) {
		return nil, fmt.Errorf("dapp: URL 需為 http(s) 且格式正確")
	}
	if category == "" {
		category = "Other"
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Unix()
	featuredFlag := 0
	if featured {
		featuredFlag = 1
	}

	// 同名同 deployer → 更新並版本遞增。
	var id int64
	var ver int64
	row := s.db.QueryRow(`SELECT id, version FROM dapps WHERE deployer=? AND name=?`, deployer, name)
	err := row.Scan(&id, &ver)
	switch {
	case err == sql.ErrNoRows:
		res, insErr := s.db.Exec(`INSERT INTO dapps
			(deployer, name, category, description, url, icon, contract, tags, version, featured, created_at, updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			deployer, name, category, description, dappURL, icon, contract, tags, 1, featuredFlag, now, now)
		if insErr != nil {
			return nil, fmt.Errorf("dapp: 寫入 DApp: %w", insErr)
		}
		id, _ = res.LastInsertId()
	case err != nil:
		return nil, fmt.Errorf("dapp: 查詢既有 DApp: %w", err)
	default:
		next := ver + 1
		if _, updErr := s.db.Exec(`UPDATE dapps SET category=?, description=?, url=?, icon=?, contract=?,
			tags=?, version=?, featured=?, updated_at=? WHERE id=?`,
			category, description, dappURL, icon, contract, tags, next, featuredFlag, now, id); updErr != nil {
			return nil, fmt.Errorf("dapp: 更新 DApp: %w", updErr)
		}
	}
	return s.getLocked(id)
}

// getLocked 呼叫方須已持鎖。
func (s *Store) getLocked(id int64) (*DApp, error) {
	row := s.db.QueryRow(`SELECT `+dappCols+` FROM dapps WHERE id=?`, id)
	d, err := scanDApp(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("dapp: DApp 不存在 (id=%d)", id)
	}
	if err != nil {
		return nil, fmt.Errorf("dapp: 查詢 DApp: %w", err)
	}
	return d, nil
}

// Get 依 id 取 DApp。
func (s *Store) Get(id int64) (*DApp, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getLocked(id)
}

// List 依參數列表（含分頁、關鍵字、分類、排序）。
func (s *Store) List(p ListParams) (*ListResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	page := p.Page
	if page < 1 {
		page = 1
	}
	size := p.Size
	if size <= 0 {
		size = 24
	}
	if size > 100 {
		size = 100
	}
	offset := (page - 1) * size

	where := []string{}
	args := []any{}
	if like := strings.TrimSpace(p.Q); like != "" {
		pat := "%" + like + "%"
		where = append(where, `(name LIKE ? OR description LIKE ? OR tags LIKE ?)`)
		args = append(args, pat, pat, pat)
	}
	if c := strings.TrimSpace(p.Category); c != "" {
		where = append(where, `category = ?`)
		args = append(args, c)
	}
	if d := strings.TrimSpace(p.Deployer); d != "" {
		where = append(where, `deployer = ?`)
		args = append(args, d)
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = " WHERE " + strings.Join(where, " AND ")
	}

	// 總數。
	var total int64
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM dapps`+whereSQL, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("dapp: 統計列表: %w", err)
	}

	order := " ORDER BY created_at DESC, id DESC"
	switch strings.ToLower(strings.TrimSpace(p.Sort)) {
	case "popular":
		order = " ORDER BY views DESC, fav_count DESC, id DESC"
	case "rating":
		order = " ORDER BY rating_avg DESC, rating_count DESC, id DESC"
	case "featured":
		order = " ORDER BY featured DESC, rating_avg DESC, id DESC"
	}

	queryArgs := append(append([]any{}, args...), size, offset)
	rows, err := s.db.Query(`SELECT `+dappCols+` FROM dapps`+whereSQL+order+` LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("dapp: 列表查詢: %w", err)
	}
	defer rows.Close()

	items := make([]DApp, 0, size)
	for rows.Next() {
		d, err := scanDApp(rows)
		if err != nil {
			return nil, fmt.Errorf("dapp: 掃描 DApp 列: %w", err)
		}
		items = append(items, *d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("dapp: 列表迭代: %w", err)
	}
	return &ListResult{Total: total, Page: page, Size: size, Items: items}, nil
}

// Categories 回傳所有分類。
func (s *Store) Categories() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT name FROM dapp_categories ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("dapp: 分類查詢: %w", err)
	}
	defer rows.Close()
	out := make([]string, 0, 8)
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AddView 瀏覽計數＋1（熱門維度）。
func (s *Store) AddView(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(`UPDATE dapps SET views=views+1 WHERE id=?`, id); err != nil {
		return fmt.Errorf("dapp: 更新瀏覽數: %w", err)
	}
	return nil
}

// Rate 評分（同 address 對同 dapp 重複＝更新），並重算平均。
func (s *Store) Rate(dappID int64, address string, score int64, comment string) (*Rating, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return nil, fmt.Errorf("dapp: 缺少評分地址")
	}
	if score < 1 || score > 5 {
		return nil, fmt.Errorf("dapp: 評分須介於 1–5")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.getLocked(dappID); err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	if _, err := s.db.Exec(`INSERT INTO dapp_ratings(dapp_id, address, score, comment, created_at)
		VALUES(?,?,?,?,?)
		ON CONFLICT(dapp_id, address) DO UPDATE SET score=excluded.score, comment=excluded.comment, created_at=excluded.created_at`,
		dappID, address, score, strings.TrimSpace(comment), now); err != nil {
		return nil, fmt.Errorf("dapp: 寫入評分: %w", err)
	}
	if err := s.recomputeRatingLocked(dappID); err != nil {
		return nil, err
	}
	return &Rating{DAppID: dappID, Address: address, Score: score,
		Comment: strings.TrimSpace(comment), CreatedAt: now}, nil
}

func (s *Store) recomputeRatingLocked(dappID int64) error {
	var (
		sum   float64
		count int64
	)
	row := s.db.QueryRow(`SELECT COALESCE(AVG(score),0), COUNT(1) FROM dapp_ratings WHERE dapp_id=?`, dappID)
	if err := row.Scan(&sum, &count); err != nil {
		return fmt.Errorf("dapp: 聚合評分: %w", err)
	}
	if _, err := s.db.Exec(`UPDATE dapps SET rating_avg=?, rating_count=? WHERE id=?`, sum, count, dappID); err != nil {
		return fmt.Errorf("dapp: 更新評分統計: %w", err)
	}
	return nil
}

// FavoriteToggle 切換收藏；回傳 (nowFavorited, dapp, error)。
func (s *Store) FavoriteToggle(dappID int64, address string) (bool, *DApp, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return false, nil, fmt.Errorf("dapp: 缺少地址")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.getLocked(dappID); err != nil {
		return false, nil, err
	}
	now := time.Now().Unix()

	var cnt int64
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM dapp_favorites WHERE dapp_id=? AND address=?`,
		dappID, address).Scan(&cnt); err != nil {
		return false, nil, fmt.Errorf("dapp: 檢查收藏: %w", err)
	}

	nowFav := false
	if cnt > 0 {
		// 取消收藏。
		if _, err := s.db.Exec(`DELETE FROM dapp_favorites WHERE dapp_id=? AND address=?`, dappID, address); err != nil {
			return false, nil, fmt.Errorf("dapp: 移除收藏: %w", err)
		}
		if _, err := s.db.Exec(`UPDATE dapps SET fav_count=MAX(fav_count-1,0) WHERE id=?`, dappID); err != nil {
			return false, nil, fmt.Errorf("dapp: 更新收藏數: %w", err)
		}
	} else {
		if _, err := s.db.Exec(`INSERT INTO dapp_favorites(dapp_id, address, created_at) VALUES(?,?,?)`,
			dappID, address, now); err != nil {
			return false, nil, fmt.Errorf("dapp: 寫入收藏: %w", err)
		}
		if _, err := s.db.Exec(`UPDATE dapps SET fav_count=fav_count+1 WHERE id=?`, dappID); err != nil {
			return false, nil, fmt.Errorf("dapp: 更新收藏數: %w", err)
		}
		nowFav = true
	}
	d, err := s.getLocked(dappID)
	if err != nil {
		return nowFav, nil, err
	}
	return nowFav, d, nil
}

// Favorites 列出某 address 收藏的 DApp。
func (s *Store) Favorites(address string) ([]DApp, error) {
	address = strings.TrimSpace(address)
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT `+prefixedDAppCols("d")+`
		FROM dapp_favorites f JOIN dapps d ON d.id=f.dapp_id
		WHERE f.address=? ORDER BY f.id DESC LIMIT 200`, address)
	if err != nil {
		return nil, fmt.Errorf("dapp: 收藏列表: %w", err)
	}
	defer rows.Close()
	out := make([]DApp, 0, 4)
	for rows.Next() {
		d, err := scanDApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// prefixedDAppCols 幫 JOIN 查詢加上表別名前綴。
func prefixedDAppCols(alias string) string {
	return alias + `.id, ` + alias + `.deployer, ` + alias + `.name, ` + alias + `.category, ` +
		alias + `.description, ` + alias + `.url, ` + alias + `.icon, ` + alias + `.contract, ` +
		alias + `.tags, ` + alias + `.version, ` + alias + `.rating_avg, ` + alias + `.rating_count, ` +
		alias + `.fav_count, ` + alias + `.views, ` + alias + `.featured, ` + alias + `.created_at, ` +
		alias + `.updated_at`
}

// parseInt64 工具（避免在 handler 層重複 strconv 細節）。
func parseInt64(s string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(s), 10, 64)
}
