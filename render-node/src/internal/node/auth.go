package node

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"tacm/internal/crypto"

	_ "modernc.org/sqlite"
)

// ===== 會員系統（搬運 Python /api/auth/* 機制）=====
//  - users 表：id/email/password_hash/created_at
//  - 密碼：PBKDF2-HMAC-SHA256（100,000 次迭代，16 bytes salt）
//  - session：HMAC-SHA256 簽名 payload，cookie "tacm_sess"（httponly、30 天）
//  - 對外：POST /api/auth/register|login|logout、GET /api/auth/me

const (
	authCookieName = "tacm_sess"
	authSecretEnv  = "TAC_AUTH_SECRET"
	pbkdf2Iters    = 100000
	pbkdf2KeyLen   = 32
)

var emailRe = regexp.MustCompile(`^[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}$`)

// AuthUser 為會員基本資料（回傳予前端）。
type AuthUser struct {
	ID         int64  `json:"id"`
	Email      string `json:"email"`
	CreatedAt  int64  `json:"created_at"`
	WalletAddr string `json:"wallet_addr"`
}

// AuthService 提供會員註冊/登入/登出與 session 驗證。
type AuthService struct {
	db     *sql.DB
	secret []byte
}

// OpenAuth 開啟會員資料庫（資料目錄下 users.db）。
func OpenAuth(dataDir string) (*AuthService, error) {
	db, err := sql.Open("sqlite", dataDir+"/users.db")
	if err != nil {
		return nil, fmt.Errorf("auth: 開啟資料庫: %w", err)
	}
	// 併發連接限制（SQLite 單寫者）。
	db.SetMaxOpenConns(4)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		email TEXT NOT NULL UNIQUE,
		password_hash TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		wallet_addr TEXT NOT NULL DEFAULT '',
		referrer TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("auth: 建立資料表: %w", err)
	}
	secret := []byte(os.Getenv(authSecretEnv))
	if len(secret) == 0 {
		// 預設密鑰（本機/演示）：正式部署務必設定 TAC_AUTH_SECRET。
		secret = []byte("tac-chain-default-secret-change-me")
	}
	return &AuthService{db: db, secret: secret}, nil
}

// Close 關閉資料庫。
func (a *AuthService) Close() error { return a.db.Close() }

func (a *AuthService) userByEmail(email string) (int64, string, error) {
	var id int64
	var hash string
	err := a.db.QueryRow(`SELECT id, password_hash FROM users WHERE email = ?`, email).Scan(&id, &hash)
	if err == sql.ErrNoRows {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", fmt.Errorf("auth: 查詢使用者: %w", err)
	}
	return id, hash, nil
}

// Register 註冊會員。email 需格式正確、密碼至少 6 碼、email 唯一。
func (a *AuthService) Register(email, password, referralCode string) (int64, string, string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if !emailRe.MatchString(email) {
		return 0, "", "", fmt.Errorf("Email 格式不正確")
	}
	if len(password) < 6 {
		return 0, "", "", fmt.Errorf("密碼至少 6 碼")
	}
	if referralCode != "" && !crypto.IsValidAddress(referralCode) {
		return 0, "", "", fmt.Errorf("邀請人地址格式不正確（應為 tx0 開頭）")
	}
	existing, _, err := a.userByEmail(email)
	if err != nil {
		return 0, "", "", err
	}
	if existing != 0 {
		return 0, "", "", fmt.Errorf("Email 已被註冊")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return 0, "", "", fmt.Errorf("auth: 產生 salt: %w", err)
	}
	hash := pbkdf2SHA256(password, salt)
	stored := "pbkdf2$" + base64.StdEncoding.EncodeToString(salt) + "$" + base64.StdEncoding.EncodeToString(hash)
	// M32：每個會員自動綁定一個鏈上錢包地址（挖礦即用此地址）。
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		return 0, "", "", fmt.Errorf("auth: 產生錢包地址: %w", err)
	}
	walletAddr, err := kp.Address()
	if err != nil {
		return 0, "", "", fmt.Errorf("auth: 產生錢包地址: %w", err)
	}
	res, err := a.db.Exec(`INSERT INTO users(email, password_hash, created_at, wallet_addr, referrer) VALUES(?,?,?,?,?)`,
		email, stored, time.Now().Unix(), walletAddr, referralCode)
	if err != nil {
		return 0, "", "", fmt.Errorf("auth: 建立使用者: %w", err)
	}
	id, _ := res.LastInsertId()
	token, err := a.SignSession(id, email)
	if err != nil {
		return 0, "", "", err
	}
	return id, token, walletAddr, nil
}

// Login 登入：驗證 email/密碼，回傳簽名 session。
func (a *AuthService) Login(email, password string) (int64, string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	id, stored, err := a.userByEmail(email)
	if err != nil {
		return 0, "", err
	}
	if id == 0 || stored == "" {
		return 0, "", fmt.Errorf("帳號或密碼錯誤")
	}
	parts := strings.Split(stored, "$")
	if len(parts) != 3 || parts[0] != "pbkdf2" {
		return 0, "", fmt.Errorf("auth: 密碼格式異常")
	}
	salt, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return 0, "", fmt.Errorf("auth: 解碼 salt: %w", err)
	}
	expect := pbkdf2SHA256(password, salt)
	got, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(expect, got) {
		return 0, "", fmt.Errorf("帳號或密碼錯誤")
	}
	token, err := a.SignSession(id, email)
	if err != nil {
		return 0, "", err
	}
	return id, token, nil
}

// SignSession 簽發 HMAC session token（payload 以 hex 前綴防注入）。
func (a *AuthService) SignSession(uid int64, email string) (string, error) {
	payload := fmt.Sprintf("%d|%s|%d", uid, email, time.Now().Unix())
	mac := hmac.New(sha256.New, a.secret)
	if _, err := mac.Write([]byte(payload)); err != nil {
		return "", fmt.Errorf("auth: 簽名失敗: %w", err)
	}
	return hex.EncodeToString(mac.Sum(nil)) + "." + base64.RawURLEncoding.EncodeToString([]byte(payload)), nil
}

// VerifySession 驗證 session token，回傳使用者資訊。
func (a *AuthService) VerifySession(token string) (*AuthUser, error) {
	dot := strings.Index(token, ".")
	if dot <= 0 {
		return nil, fmt.Errorf("session 格式錯誤")
	}
	sigHex, b64 := token[:dot], token[dot+1:]
	payload, err := base64.RawURLEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("session 格式錯誤")
	}
	mac := hmac.New(sha256.New, a.secret)
	if _, err := mac.Write(payload); err != nil {
		return nil, fmt.Errorf("auth: 驗證失敗: %w", err)
	}
	if !hmac.Equal([]byte(sigHex), []byte(hex.EncodeToString(mac.Sum(nil)))) {
		return nil, fmt.Errorf("session 無效")
	}
	parts := strings.SplitN(string(payload), "|", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("session 無效")
	}
	var uid int64
	if _, err := fmt.Sscanf(parts[0], "%d", &uid); err != nil {
		return nil, fmt.Errorf("session 無效")
	}
	var createdAt int64
	var walletAddr string
	if err := a.db.QueryRow(`SELECT created_at, wallet_addr FROM users WHERE id=?`, uid).Scan(&createdAt, &walletAddr); err != nil {
		return nil, fmt.Errorf("session 使用者不存在")
	}
	return &AuthUser{ID: uid, Email: parts[1], CreatedAt: createdAt, WalletAddr: walletAddr}, nil
}

// CurrentUser 從請求 cookie 解析目前登入使用者（未登入回傳 nil）。
func (a *AuthService) CurrentUser(r *http.Request) (*AuthUser, error) {
	c, err := r.Cookie(authCookieName)
	if err != nil {
		return nil, nil
	}
	return a.VerifySession(c.Value)
}

// SetAuthCookie 於響應設定登入 cookie（30 天、httponly、samesite=lax）。
func SetAuthCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   60 * 60 * 24 * 30,
	})
}

// ClearAuthCookie 清除登入 cookie。
func ClearAuthCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

// pbkdf2SHA256 以 PBKDF2-HMAC-SHA256 推導密碼金鑰（與 Python 100,000 迭代一致）。
func pbkdf2SHA256(password string, salt []byte) []byte {
	dk := make([]byte, pbkdf2KeyLen)
	u := hmac.New(sha256.New, []byte(password))
	u.Write(salt)
	u.Write([]byte{1, 0, 0, 0})
	t := u.Sum(nil)
	copy(dk, t)
	for i := 1; i < pbkdf2Iters; i++ {
		u = hmac.New(sha256.New, []byte(password))
		u.Write(t)
		t = u.Sum(nil)
		for j := 0; j < len(dk); j++ {
			dk[j] ^= t[j]
		}
	}
	return dk
}

// marshalJSON 統一回傳 JSON。
func marshalJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{"ok":false,"message":"JSON 編碼失敗"}`)
	}
	return b
}
