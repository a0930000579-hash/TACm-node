package community

// community.go — 內建 Facebook 風社群（搬運自原本 community_app/community_db 並優化）：
//   動態牆（post）、按讚、留言、市集（kind=market、TACM 計價）、廣告池。
//   身份＝錢包地址（Go 版無帳密系統，以 tx0 地址代表成員）。

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store 社群資料庫。
type Store struct {
	db *sql.DB
}

const communitySchema = `
CREATE TABLE IF NOT EXISTS posts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  address TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'post',
  content TEXT,
  image_url TEXT,
  video_url TEXT,
  created_at INTEGER NOT NULL,
  mine_id TEXT DEFAULT NULL,
  price_tacm REAL DEFAULT NULL,
  sold INTEGER NOT NULL DEFAULT 0,
  buyer TEXT DEFAULT NULL
);
CREATE INDEX IF NOT EXISTS idx_posts_created ON posts(created_at DESC);
CREATE TABLE IF NOT EXISTS comments (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  post_id INTEGER NOT NULL,
  address TEXT NOT NULL,
  body TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS likes (
  post_id INTEGER NOT NULL,
  address TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (post_id, address)
);
CREATE TABLE IF NOT EXISTS reviews (
  post_id INTEGER NOT NULL,
  address TEXT NOT NULL,
  rating INTEGER NOT NULL CHECK (rating BETWEEN 1 AND 5),
  comment TEXT,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (post_id, address)
);
CREATE TABLE IF NOT EXISTS profiles (
  address TEXT PRIMARY KEY,
  display_name TEXT,
  avatar_url TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS ads (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  address TEXT NOT NULL,
  title TEXT NOT NULL,
  body TEXT,
  media_url TEXT,
  budget_tacm REAL NOT NULL DEFAULT 0,
  daily_budget_tacm REAL NOT NULL DEFAULT 0,
  placements TEXT NOT NULL DEFAULT 'feed',
  target TEXT,
  status TEXT NOT NULL DEFAULT 'pending',
  impressions INTEGER NOT NULL DEFAULT 0,
  clicks INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS follows (
  follower TEXT NOT NULL,
  followee TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (follower, followee)
);
CREATE INDEX IF NOT EXISTS idx_follows_followee ON follows(followee);
CREATE TABLE IF NOT EXISTS rewards (
  address TEXT PRIMARY KEY,
  points INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS reward_meta (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  period INTEGER NOT NULL DEFAULT 0,
  last_tip INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS live_rooms (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  owner TEXT NOT NULL,
  title TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'live',
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS live_messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  room_id INTEGER NOT NULL,
  address TEXT NOT NULL,
  body TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_live_msg_room ON live_messages(room_id, id);
`

// Open 打開（或創建）社群資料庫。
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("community: 建立數據目錄: %w", err)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)",
		filepath.Join(dataDir, "community.db"))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("community: 開啟數據庫: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(communitySchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("community: 初始化 schema: %w", err)
	}
	// 相容遷移：舊 posts 表無 sold/buyer 欄位時補上（SQLite 無 ADD COLUMN IF NOT EXISTS）。
	if err := migrateColumns(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// migrateColumns 檢查並補齊 posts.sold / posts.buyer 欄位（舊庫相容）。
func migrateColumns(db *sql.DB) error {
	cols := map[string]bool{}
	rows, err := db.Query(`PRAGMA table_info(posts)`)
	if err != nil {
		return fmt.Errorf("community: 檢查欄位: %w", err)
	}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			_ = rows.Close()
			return fmt.Errorf("community: 讀取欄位: %w", err)
		}
		cols[name] = true
	}
	_ = rows.Close()
	if !cols["video_url"] {
		if _, err := db.Exec(`ALTER TABLE posts ADD COLUMN video_url TEXT`); err != nil {
			return fmt.Errorf("community: 新增 video_url 欄位: %w", err)
		}
	}
	if !cols["sold"] {
		if _, err := db.Exec(`ALTER TABLE posts ADD COLUMN sold INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("community: 遷移 sold 欄位: %w", err)
		}
	}
	if !cols["buyer"] {
		if _, err := db.Exec(`ALTER TABLE posts ADD COLUMN buyer TEXT DEFAULT NULL`); err != nil {
			return fmt.Errorf("community: 遷移 buyer 欄位: %w", err)
		}
	}
	return nil
}

// Close 關閉社群資料庫。
func (s *Store) Close() error { return s.db.Close() }

// Review 市集評價視圖。
type Review struct {
	PostID    int64  `json:"post_id"`
	Address   string `json:"address"`
	Rating    int    `json:"rating"`
	Comment   string `json:"comment"`
	CreatedAt int64  `json:"created_at"`
}

// AddReview 新增（或更新）市集評價：僅限 kind=market 貼文、rating 1-5；同一買家對同一貼文只保留最新一則。
func (s *Store) AddReview(postID int64, address string, rating int, comment string) error {
	if rating < 1 || rating > 5 {
		return fmt.Errorf("community: 評分必須在 1-5 之間")
	}
	if strings.TrimSpace(address) == "" {
		return fmt.Errorf("community: 缺少評價人地址")
	}
	var kind string
	if err := s.db.QueryRow(`SELECT kind FROM posts WHERE id=?`, postID).Scan(&kind); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("community: 貼文不存在")
		}
		return fmt.Errorf("community: 查詢貼文: %w", err)
	}
	if kind != "market" {
		return fmt.Errorf("community: 僅市集貼文可評價")
	}
	_, err := s.db.Exec(`INSERT INTO reviews (post_id, address, rating, comment, created_at)
		VALUES (?,?,?,?,?)
		ON CONFLICT(post_id, address) DO UPDATE SET rating=excluded.rating, comment=excluded.comment, created_at=excluded.created_at`,
		postID, address, rating, strings.TrimSpace(comment), time.Now().Unix())
	if err != nil {
		return fmt.Errorf("community: 寫入評價: %w", err)
	}
	return nil
}

// ListReviews 列出某市集貼文的全部評價。
func (s *Store) ListReviews(postID int64) ([]Review, error) {
	rows, err := s.db.Query(`SELECT post_id, address, rating, comment, created_at FROM reviews WHERE post_id=? ORDER BY created_at DESC`, postID)
	if err != nil {
		return nil, fmt.Errorf("community: 讀取評價: %w", err)
	}
	defer rows.Close()
	out := []Review{}
	for rows.Next() {
		var r Review
		var comment sql.NullString
		if err := rows.Scan(&r.PostID, &r.Address, &r.Rating, &comment, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("community: 掃描評價: %w", err)
		}
		r.Comment = comment.String
		out = append(out, r)
	}
	return out, rows.Err()
}

// PostRating 回傳市集貼文平均評分與評價數。
func (s *Store) PostRating(postID int64) (float64, int, error) {
	var avg sql.NullFloat64
	var n int
	if err := s.db.QueryRow(`SELECT AVG(rating), COUNT(*) FROM reviews WHERE post_id=?`, postID).Scan(&avg, &n); err != nil {
		return 0, 0, fmt.Errorf("community: 統計評價: %w", err)
	}
	return avg.Float64, n, nil
}

// Post 貼文視圖（含讚/留言數與市集成交狀態）。
type Post struct {
	ID        int64    `json:"id"`
	Address   string   `json:"address"`
	Kind      string   `json:"kind"`
	Content   string   `json:"content"`
	ImageURL  *string  `json:"image_url,omitempty"`
	VideoURL  *string  `json:"video_url,omitempty"`
	CreatedAt int64    `json:"created_at"`
	MineID    *string  `json:"mine_id,omitempty"`
	PriceTACM *float64 `json:"price_tacm,omitempty"`
	Sold      bool     `json:"sold"`
	Buyer     string   `json:"buyer,omitempty"`
	Likes      int64  `json:"likes"`
	Comments   int64  `json:"comments"`
	Author     string `json:"author"` // 地址（前端虛化顯示）
	AuthorName string `json:"author_name,omitempty"` // M100：作者對外顯示名稱
	AvatarURL  string `json:"avatar_url,omitempty"`  // M100：作者頭像
}

// Comment 留言視圖。
type Comment struct {
	ID        int64  `json:"id"`
	PostID    int64  `json:"post_id"`
	Address   string `json:"address"`
	Body      string `json:"body"`
	CreatedAt int64  `json:"created_at"`
}

// Ad 廣告視圖（M97：FB/YT 式投放——媒體封面、每日預算、投放位置、曝光/點擊計數）。
type Ad struct {
	ID              int64   `json:"id"`
	Address         string  `json:"address"`
	Title           string  `json:"title"`
	Body            string  `json:"body"`
	MediaURL        string  `json:"media_url"`
	BudgetTACM      float64 `json:"budget_tacm"`
	DailyBudgetTACM float64 `json:"daily_budget_tacm"`
	Placements      string  `json:"placements"`
	Target          string  `json:"target"`
	Status          string  `json:"status"`
	Impressions     int64   `json:"impressions"`
	Clicks          int64   `json:"clicks"`
	CreatedAt       int64   `json:"created_at"`
}

// Stats 社群統計。
type Stats struct {
	Posts    int64 `json:"posts"`
	Comments int64 `json:"comments"`
	Likes    int64 `json:"likes"`
	Ads      int64 `json:"ads"`
	Members  int64 `json:"members"` // 發文/留言/按讚的去重地址數
}

const postCols = `p.id, p.address, p.kind, COALESCE(p.content,''), p.image_url, p.video_url, p.created_at, p.mine_id, p.price_tacm, p.sold, COALESCE(p.buyer,''),
 (SELECT COUNT(1) FROM likes l WHERE l.post_id=p.id) AS likes,
 (SELECT COUNT(1) FROM comments c WHERE c.post_id=p.id) AS comments,
 COALESCE(pr.display_name,''), COALESCE(pr.avatar_url,'')`

func scanPost(sc interface{ Scan(...any) error }) (*Post, error) {
	var p Post
	var image, video, mine, buyer, dname, ava sql.NullString
	var price sql.NullFloat64
	if err := sc.Scan(&p.ID, &p.Address, &p.Kind, &p.Content, &image, &video, &p.CreatedAt, &mine, &price,
		&p.Sold, &buyer, &p.Likes, &p.Comments, &dname, &ava); err != nil {
		return nil, err
	}
	if dname.Valid && dname.String != "" {
		p.AuthorName = dname.String
	}
	if ava.Valid {
		p.AvatarURL = ava.String
	}
	if image.Valid {
		p.ImageURL = &image.String
	}
	if video.Valid {
		p.VideoURL = &video.String
	}
	if mine.Valid {
		p.MineID = &mine.String
	}
	if price.Valid {
		p.PriceTACM = &price.Float64
	}
	if buyer.Valid && buyer.String != "" {
		p.Buyer = buyer.String
	}
	p.Author = p.Address
	return &p, nil
}

// Feed 動態牆（全部貼文，最新在前）。
func (s *Store) Feed(limit, offset int) ([]Post, error) {
	if limit <= 0 {
		limit = 30
	}
	rows, err := s.db.Query(`SELECT `+postCols+` FROM posts p LEFT JOIN profiles pr ON pr.address=p.address ORDER BY p.created_at DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("community: 讀取動態: %w", err)
	}
	defer rows.Close()
	out := []Post{}
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, fmt.Errorf("community: 掃描貼文: %w", err)
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// CreatePost 發文（kind=post|market）。
func (s *Store) CreatePost(address, kind, content string, imageURL, videoURL string, priceTACM *float64) (int64, error) {
	// M90：富文本貼文一律過白名單 sanitizer（阻斷 XSS）。
	content = sanitizeRichText(content)
	if address == "" {
		return 0, fmt.Errorf("community: 缺少成員地址")
	}
	if content == "" && imageURL == "" {
		return 0, fmt.Errorf("community: 內容不可為空")
	}
	var img any
	if imageURL != "" {
		img = imageURL
	}
	var vid any
	if videoURL != "" {
		vid = videoURL
	}
	res, err := s.db.Exec(`INSERT INTO posts(address, kind, content, image_url, video_url, created_at, price_tacm) VALUES(?,?,?,?,?,?,?)`,
		address, kind, content, img, vid, time.Now().Unix(), priceTACM)
	if err != nil {
		return 0, fmt.Errorf("community: 發文: %w", err)
	}
	return res.LastInsertId()
}

// Like 按讚（INSERT OR IGNORE 冪等）。
func (s *Store) Like(postID int64, address string) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO likes(post_id, address, created_at) VALUES(?,?,?)`,
		postID, address, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("community: 按讚: %w", err)
	}
	return nil
}

// Comments 留言列表（舊→新）。
func (s *Store) Comments(postID int64, limit int) ([]Comment, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT id, post_id, address, body, created_at FROM comments WHERE post_id=? ORDER BY created_at ASC LIMIT ?`, postID, limit)
	if err != nil {
		return nil, fmt.Errorf("community: 讀取留言: %w", err)
	}
	defer rows.Close()
	out := []Comment{}
	for rows.Next() {
		var c Comment
		if err := rows.Scan(&c.ID, &c.PostID, &c.Address, &c.Body, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AddComment 留言。
func (s *Store) AddComment(postID int64, address, body string) error {
	if body == "" {
		return fmt.Errorf("community: 留言不可為空")
	}
	_, err := s.db.Exec(`INSERT INTO comments(post_id, address, body, created_at) VALUES(?,?,?,?)`,
		postID, address, body, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("community: 留言: %w", err)
	}
	return nil
}

// Market 市集列表（kind=market）。
func (s *Store) Market(limit int) ([]Post, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT `+postCols+` FROM posts p LEFT JOIN profiles pr ON pr.address=p.address WHERE p.kind='market' ORDER BY p.created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("community: 讀取市集: %w", err)
	}
	defer rows.Close()
	out := []Post{}
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// GetPost 單篇貼文（buy-intent 用）。
func (s *Store) GetPost(id int64) (*Post, error) {
	row := s.db.QueryRow(`SELECT `+postCols+` FROM posts p LEFT JOIN profiles pr ON pr.address=p.address WHERE p.id=?`, id)
	p, err := scanPost(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("community: 查詢貼文: %w", err)
	}
	return p, nil
}

// MarkSold 原子成交標記：僅在商品尚未售出時成功（防重複購買）；回傳是否成功。
func (s *Store) MarkSold(id int64, buyer string) (bool, error) {
	res, err := s.db.Exec(`UPDATE posts SET sold=1, buyer=? WHERE id=? AND sold=0`, buyer, id)
	if err != nil {
		return false, fmt.Errorf("community: 成交標記: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("community: 成交標記影響行數: %w", err)
	}
	return n > 0, nil
}

// UnmarkSold 成交回滾（付款失敗時釋放商品）。
func (s *Store) UnmarkSold(id int64, buyer string) error {
	if _, err := s.db.Exec(`UPDATE posts SET sold=0, buyer=NULL WHERE id=? AND buyer=?`, id, buyer); err != nil {
		return fmt.Errorf("community: 成交回滾: %w", err)
	}
	return nil
}

// Ads 廣告池（pending=待付款、active=投放中；最新在前）。
func (s *Store) Ads(limit int) ([]Ad, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT id, address, title, body, media_url, budget_tacm, daily_budget_tacm, placements, target, status, impressions, clicks, created_at FROM ads ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("community: 讀取廣告: %w", err)
	}
	defer rows.Close()
	out := []Ad{}
	for rows.Next() {
		var a Ad
		if err := rows.Scan(&a.ID, &a.Address, &a.Title, &a.Body, &a.MediaURL, &a.BudgetTACM, &a.DailyBudgetTACM, &a.Placements, &a.Target, &a.Status, &a.Impressions, &a.Clicks, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CreateAd 發佈廣告（預設 pending：付款後才 active）。M97：支援媒體封面、每日預算、投放位置。
func (s *Store) CreateAd(address, title, body, mediaURL, target, placements string, budget, dailyBudget float64) (int64, error) {
	if title == "" || budget <= 0 {
		return 0, fmt.Errorf("community: 請填寫標題與預算")
	}
	if placements == "" {
		placements = "feed"
	}
	res, err := s.db.Exec(`INSERT INTO ads(address, title, body, media_url, budget_tacm, daily_budget_tacm, placements, target, status, created_at) VALUES(?,?,?,?,?,?,?,?,'pending',?)`,
		address, title, body, mediaURL, budget, dailyBudget, placements, target, time.Now().Unix())
	if err != nil {
		return 0, fmt.Errorf("community: 發佈廣告: %w", err)
	}
	return res.LastInsertId()
}

// ActiveAds 投放中廣告（M97）：按位置篩選（feed/video/all），低曝光優先（公平輪播，FB/YT 式）。
func (s *Store) ActiveAds(placements string, limit int) ([]Ad, error) {
	if limit <= 0 {
		limit = 10
	}
	q := `SELECT id, address, title, body, media_url, budget_tacm, daily_budget_tacm, placements, target, status, impressions, clicks, created_at FROM ads WHERE status='active'`
	var args []any
	if placements != "" && placements != "all" {
		q += ` AND (placements=? OR placements='all')`
		args = append(args, placements)
	}
	q += ` ORDER BY impressions ASC, id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("community: 讀取投放廣告: %w", err)
	}
	defer rows.Close()
	out := []Ad{}
	for rows.Next() {
		var a Ad
		if err := rows.Scan(&a.ID, &a.Address, &a.Title, &a.Body, &a.MediaURL, &a.BudgetTACM, &a.DailyBudgetTACM, &a.Placements, &a.Target, &a.Status, &a.Impressions, &a.Clicks, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AdImpression 廣告曝光＋1（FB/YT 式曝光計費與輪播）。
func (s *Store) AdImpression(id int64) error {
	if _, err := s.db.Exec(`UPDATE ads SET impressions=impressions+1 WHERE id=?`, id); err != nil {
		return fmt.Errorf("community: 廣告曝光計數: %w", err)
	}
	return nil
}

// AdClick 廣告點擊＋1。
func (s *Store) AdClick(id int64) error {
	if _, err := s.db.Exec(`UPDATE ads SET clicks=clicks+1 WHERE id=?`, id); err != nil {
		return fmt.Errorf("community: 廣告點擊計數: %w", err)
	}
	return nil
}

// GetAd 單筆廣告（付款驗證用）。
func (s *Store) GetAd(id int64) (*Ad, error) {
	row := s.db.QueryRow(`SELECT id, address, title, body, media_url, budget_tacm, daily_budget_tacm, placements, target, status, impressions, clicks, created_at FROM ads WHERE id=?`, id)
	var a Ad
	if err := row.Scan(&a.ID, &a.Address, &a.Title, &a.Body, &a.MediaURL, &a.BudgetTACM, &a.DailyBudgetTACM, &a.Placements, &a.Target, &a.Status, &a.Impressions, &a.Clicks, &a.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("community: 查詢廣告: %w", err)
	}
	return &a, nil
}

// MarkAdPaid 廣告付款完成 → active。
func (s *Store) MarkAdPaid(id int64) error {
	if _, err := s.db.Exec(`UPDATE ads SET status='active' WHERE id=?`, id); err != nil {
		return fmt.Errorf("community: 廣告付款標記: %w", err)
	}
	return nil
}

// Stats 社群統計（去重成員）。
func (s *Store) Stats() (*Stats, error) {
	var st Stats
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM posts`).Scan(&st.Posts); err != nil {
		return nil, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM comments`).Scan(&st.Comments); err != nil {
		return nil, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM likes`).Scan(&st.Likes); err != nil {
		return nil, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM ads`).Scan(&st.Ads); err != nil {
		return nil, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(DISTINCT address) FROM (SELECT address FROM posts UNION SELECT address FROM comments UNION SELECT address FROM likes)`).Scan(&st.Members); err != nil {
		return nil, err
	}
	return &st, nil
}

// ---------- M73-B：關注（動態牆） ----------

// Follow 追蹤一位成員（冪等）。
func (s *Store) Follow(follower, followee string) error {
	if follower == "" || followee == "" {
		return fmt.Errorf("community: 追蹤參數缺失")
	}
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO follows(follower,followee,created_at) VALUES(?,?,?)`,
		follower, followee, time.Now().Unix()); err != nil {
		return fmt.Errorf("community: 追蹤失敗: %w", err)
	}
	return nil
}

// Unfollow 取消追蹤。
func (s *Store) Unfollow(follower, followee string) error {
	if _, err := s.db.Exec(`DELETE FROM follows WHERE follower=? AND followee=?`, follower, followee); err != nil {
		return fmt.Errorf("community: 取消追蹤失敗: %w", err)
	}
	return nil
}

// Following 列出已追蹤的成員（依追蹤時間新→舊）。
func (s *Store) Following(follower string) ([]string, error) {
	rows, err := s.db.Query(`SELECT followee FROM follows WHERE follower=? ORDER BY created_at DESC`, follower)
	if err != nil {
		return nil, fmt.Errorf("community: 查詢追蹤列表: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// IsFollowing 是否已追蹤。
func (s *Store) IsFollowing(follower, followee string) (bool, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM follows WHERE follower=? AND followee=?`, follower, followee).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// FollowingFeed 動態牆：只顯示已追蹤成員的貼文（含自己的）。
func (s *Store) FollowingFeed(follower string, limit, offset int) ([]Post, error) {
	if limit <= 0 {
		limit = 30
	}
	rows, err := s.db.Query(`SELECT `+postCols+` FROM posts p LEFT JOIN profiles pr ON pr.address=p.address
		WHERE p.address=? OR p.address IN (SELECT followee FROM follows WHERE follower=?)
		ORDER BY p.created_at DESC LIMIT ? OFFSET ?`, follower, follower, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("community: 追蹤動態牆: %w", err)
	}
	defer rows.Close()
	out := []Post{}
	for rows.Next() {
		var p Post
		var vid, dname, ava sql.NullString
		if err := rows.Scan(&p.ID, &p.Address, &p.Kind, &p.Content, &p.ImageURL, &vid, &p.CreatedAt, &p.MineID, &p.PriceTACM, &p.Sold, &p.Buyer, &p.Likes, &p.Comments, &dname, &ava); err != nil {
			return nil, err
		}
		if vid.Valid {
			p.VideoURL = &vid.String
		}
		if dname.Valid {
			p.AuthorName = dname.String
		}
		if ava.Valid {
			p.AvatarURL = ava.String
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---------- M73-B：互動積分與鏈上獎勵結算 ----------

// AddPoints 累積互動積分（發文/按讚/留言/成交等）。
func (s *Store) AddPoints(address string, pts int64) error {
	if address == "" || pts <= 0 {
		return nil
	}
	if _, err := s.db.Exec(`INSERT INTO rewards(address,points,updated_at) VALUES(?,?,?)
		ON CONFLICT(address) DO UPDATE SET points=points+?, updated_at=?`,
		address, pts, time.Now().Unix(), pts, time.Now().Unix()); err != nil {
		return fmt.Errorf("community: 累積積分: %w", err)
	}
	return nil
}

// Points 查詢成員積分。
func (s *Store) Points(address string) (int64, error) {
	var p int64
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(points),0) FROM rewards WHERE address=?`, address).Scan(&p); err != nil {
		return 0, fmt.Errorf("community: 查詢積分: %w", err)
	}
	return p, nil
}

// RewardRow 積分明細（結算與排行共用）。
type RewardRow struct {
	Address string `json:"address"`
	Points  int64  `json:"points"`
}

// TopRewards 積分排行（前 N）。
func (s *Store) TopRewards(limit int) ([]RewardRow, error) {
	rows, err := s.db.Query(`SELECT address, points FROM rewards WHERE points>0 ORDER BY points DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("community: 積分排行: %w", err)
	}
	defer rows.Close()
	out := []RewardRow{}
	for rows.Next() {
		var r RewardRow
		if err := rows.Scan(&r.Address, &r.Points); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SnapshotAndClearRewards 結算快照：讀取全部積分並清零（返回有積分者）。
// 快照與清零在同一事務，保證「每期只結算一次」。
func (s *Store) SnapshotAndClearRewards() ([]RewardRow, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT address, points FROM rewards WHERE points>0`)
	if err != nil {
		return nil, err
	}
	out := []RewardRow{}
	for rows.Next() {
		var r RewardRow
		if err := rows.Scan(&r.Address, &r.Points); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	_ = rows.Close()
	if len(out) > 0 {
		if _, err := tx.Exec(`UPDATE rewards SET points=0, updated_at=?`, time.Now().Unix()); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// RewardMeta 上一期結算資訊。
type RewardMeta struct {
	Period  int64 `json:"period"`
	LastTip int64 `json:"last_tip"`
}

// GetRewardMeta 讀取結算期號與上次結算鏈高。
func (s *Store) GetRewardMeta() (RewardMeta, error) {
	var m RewardMeta
	if err := s.db.QueryRow(`SELECT period, last_tip FROM reward_meta WHERE id=1`).Scan(&m.Period, &m.LastTip); err != nil {
		if err == sql.ErrNoRows {
			return RewardMeta{}, nil
		}
		return m, err
	}
	return m, nil
}

// SetRewardMeta 更新結算期號與鏈高。
func (s *Store) SetRewardMeta(period, lastTip int64) error {
	if _, err := s.db.Exec(`INSERT INTO reward_meta(id,period,last_tip) VALUES(1,?,?)
		ON CONFLICT(id) DO UPDATE SET period=?, last_tip=?`, period, lastTip, period, lastTip); err != nil {
		return fmt.Errorf("community: 更新結算期號: %w", err)
	}
	return nil
}

// ---------- M95：好友與通知中心 ----------

// Followers 列出追蹤此成員的人（依追蹤時間新→舊）。
func (s *Store) Followers(followee string) ([]string, error) {
	rows, err := s.db.Query(`SELECT follower FROM follows WHERE followee=? ORDER BY created_at DESC`, followee)
	if err != nil {
		return nil, fmt.Errorf("community: 查詢追蹤者列表: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Notification 社群通知項目（讚/留言/追蹤/積分）。
type Notification struct {
	Type      string `json:"type"` // like|comment|follow|points
	Actor     string `json:"actor"`
	PostID    int64  `json:"post_id"`
	Content   string `json:"content"`
	CreatedAt int64  `json:"created_at"`
}

// Notifications 聚合與 addr 相關的活動通知（新→舊）。
func (s *Store) Notifications(addr string, limit int) ([]Notification, error) {
	if limit <= 0 {
		limit = 50
	}
	out := []Notification{}
	// 1) 我的貼文被按讚
	rows, err := s.db.Query(`SELECT 'like', l.address, p.id, COALESCE(p.content,''), l.created_at
		FROM likes l JOIN posts p ON p.id=l.post_id
		WHERE p.address=? AND l.address<>? ORDER BY l.created_at DESC LIMIT ?`, addr, addr, limit)
	if err != nil {
		return nil, fmt.Errorf("community: 通知查詢(likes): %w", err)
	}
	for rows.Next() {
		var n Notification
		if err := rows.Scan(&n.Type, &n.Actor, &n.PostID, &n.Content, &n.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, n)
	}
	rows.Close()
	// 2) 我的貼文被留言
	rows, err = s.db.Query(`SELECT 'comment', c.address, p.id, c.body, c.created_at
		FROM comments c JOIN posts p ON p.id=c.post_id
		WHERE p.address=? AND c.address<>? ORDER BY c.created_at DESC LIMIT ?`, addr, addr, limit)
	if err != nil {
		return nil, fmt.Errorf("community: 通知查詢(comments): %w", err)
	}
	for rows.Next() {
		var n Notification
		if err := rows.Scan(&n.Type, &n.Actor, &n.PostID, &n.Content, &n.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, n)
	}
	rows.Close()
	// 3) 被追蹤
	rows, err = s.db.Query(`SELECT 'follow', follower, 0, '', created_at FROM follows
		WHERE followee=? AND follower<>? ORDER BY created_at DESC LIMIT ?`, addr, addr, limit)
	if err != nil {
		return nil, fmt.Errorf("community: 通知查詢(follows): %w", err)
	}
	for rows.Next() {
		var n Notification
		if err := rows.Scan(&n.Type, &n.Actor, &n.PostID, &n.Content, &n.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, n)
	}
	rows.Close()
	// 4) 積分獎勵（M73-B：互動積分結算）
	rows, err = s.db.Query(`SELECT 'points', '', 0, '互動積分獎勵 +'||points||' 分', updated_at FROM rewards
		WHERE address=? AND points>0 ORDER BY updated_at DESC LIMIT ?`, addr, limit)
	if err != nil {
		return nil, fmt.Errorf("community: 通知查詢(points): %w", err)
	}
	for rows.Next() {
		var n Notification
		if err := rows.Scan(&n.Type, &n.Actor, &n.PostID, &n.Content, &n.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, n)
	}
	rows.Close()
	// 合併排序（新→舊）
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Profile 社群個人資料（對外顯示名稱＋頭像）。
type Profile struct {
	Address     string `json:"address"`
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url"`
}

// SetProfile 設定/更新個人資料（upsert）。
func (s *Store) SetProfile(address, displayName, avatarURL string) error {
	now := time.Now().Unix()
	_, err := s.db.Exec(`INSERT INTO profiles (address, display_name, avatar_url, created_at, updated_at)
		VALUES (?,?,?,?,?)
		ON CONFLICT(address) DO UPDATE SET display_name=?, avatar_url=?, updated_at=?`,
		address, displayName, avatarURL, now, now, displayName, avatarURL, now)
	if err != nil {
		return fmt.Errorf("community: 更新個人資料: %w", err)
	}
	return nil
}

// Profile 讀取個人資料（不存在回空 Profile）。
func (s *Store) Profile(address string) (*Profile, error) {
	var pr Profile
	var dn, av sql.NullString
	err := s.db.QueryRow(`SELECT address, display_name, avatar_url FROM profiles WHERE address=?`, address).
		Scan(&pr.Address, &dn, &av)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &Profile{Address: address}, nil
		}
		return nil, fmt.Errorf("community: 讀取個人資料: %w", err)
	}
	if dn.Valid {
		pr.DisplayName = dn.String
	}
	if av.Valid {
		pr.AvatarURL = av.String
	}
	return &pr, nil
}

// UpdatePostContent 編輯貼文（僅作者可改；回傳 ok=false 表示非作者或不存在）。
func (s *Store) UpdatePostContent(id int64, address, content string) (bool, error) {
	res, err := s.db.Exec(`UPDATE posts SET content=? WHERE id=? AND address=?`, content, id, address)
	if err != nil {
		return false, fmt.Errorf("community: 編輯貼文: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// DeletePost 刪除貼文（僅作者可刪；同時清除留言/讚）。
func (s *Store) DeletePost(id int64, address string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM posts WHERE id=? AND address=?`, id, address)
	if err != nil {
		return false, fmt.Errorf("community: 刪除貼文: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	if _, err := s.db.Exec(`DELETE FROM comments WHERE post_id=?`, id); err != nil {
		return false, fmt.Errorf("community: 清除留言: %w", err)
	}
	if _, err := s.db.Exec(`DELETE FROM likes WHERE post_id=?`, id); err != nil {
		return false, fmt.Errorf("community: 清除讚: %w", err)
	}
	return true, nil
}
