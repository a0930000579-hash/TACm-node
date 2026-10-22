package node

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
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

// web3ChallengeRe 校驗以太坊地址（0x + 40 hex）。
var ethAddrRe = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

// web3NonceTTL：SIWE 挑戰有效時間（5 分鐘）。
const web3NonceTTL = 5 * time.Minute

// web3Challenge 記憶體中的一次性 SIWE 挑戰。
type web3Challenge struct {
	Address   string // 小寫 0x 地址
	Message   string // 完整 EIP-4361 訊息
	ExpiresAt int64  // unix 秒
	Used      bool
}

// AuthUser 為會員基本資料（回傳予前端）。
type AuthUser struct {
	ID         int64  `json:"id"`
	Email      string `json:"email"`
	CreatedAt  int64  `json:"created_at"`
	WalletAddr string `json:"wallet_addr"`
	IsAdmin    bool   `json:"is_admin"`
}

// AuthService 提供會員註冊/登入/登出與 session 驗證。
type AuthService struct {
	db     *sql.DB
	secret []byte

	// Web3（SIWE）：記憶體一次性 nonce 挑戰（AuthService 生命週期）。
	web3NonceMu sync.Mutex
	web3Nonces  map[string]web3Challenge
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
	// v127：管理員角色欄位（冪等遷移：欄位已存在則跳過）。
	if _, err := db.Exec("ALTER TABLE users ADD COLUMN is_admin INTEGER NOT NULL DEFAULT 0"); err != nil && !strings.Contains(err.Error(), "duplicate column") && !strings.Contains(err.Error(), "already exists") {
		db.Close()
		return nil, fmt.Errorf("auth: 建立管理員欄位: %w", err)
	}
	secret := []byte(os.Getenv(authSecretEnv))
	if len(secret) == 0 {
		// 預設密鑰（本機/演示）：正式部署務必設定 TAC_AUTH_SECRET。
		secret = []byte("tac-chain-default-secret-change-me")
	}
	return &AuthService{
		db:         db,
		secret:     secret,
		web3Nonces: map[string]web3Challenge{},
	}, nil
}

// Close 關閉資料庫。
func (a *AuthService) Close() error { return a.db.Close() }

// SetAdmin 設定/取消管理員角色（僅管理員可呼叫——由呼叫方授權檢查）。
func (a *AuthService) SetAdmin(email string, isAdmin bool) error {
	if email == "" {
		return errors.New("auth: email 不能為空")
	}
	v := 0
	if isAdmin {
		v = 1
	}
	res, err := a.db.Exec(`UPDATE users SET is_admin=? WHERE email=?`, v, email)
	if err != nil {
		return fmt.Errorf("auth: 設定管理員: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("auth: 使用者不存在")
	}
	return nil
}

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
	var isAdmin int
	if err := a.db.QueryRow(`SELECT created_at, wallet_addr, is_admin FROM users WHERE id=?`, uid).Scan(&createdAt, &walletAddr, &isAdmin); err != nil {
		return nil, fmt.Errorf("session 使用者不存在")
	}
	return &AuthUser{ID: uid, Email: parts[1], CreatedAt: createdAt, WalletAddr: walletAddr, IsAdmin: isAdmin == 1}, nil
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

// ===== Web3 錢包簽名登入（EIP-4361 Sign-In With Ethereum / EIP-191）=====

// web3RandomNonce 產生 ≥16 字元 alphanumeric 隨機 nonce（crypto/rand）。
func web3RandomNonce() (string, error) {
	const letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = letters[int(b[i])%len(letters)]
	}
	return string(b), nil
}

// NewWeb3Challenge 依 EIP-4361 組出登入訊息，並暫存一次性 nonce。
//   - domain  由呼叫端傳入（HTTP handler 取 r.Host）
//   - chainID 由前端傳入，預設 "1"
//
// 回傳完整 message 與 nonce。
func (a *AuthService) NewWeb3Challenge(address, domain, chainID string) (string, string, error) {
	addr := strings.ToLower(strings.TrimSpace(address))
	if !ethAddrRe.MatchString(addr) {
		return "", "", fmt.Errorf("auth: 地址格式應為 0x + 40 hex")
	}
	if domain == "" {
		domain = "localhost"
	}
	if chainID == "" {
		chainID = "1"
	}
	nonce, err := web3RandomNonce()
	if err != nil {
		return "", "", fmt.Errorf("auth: 產生 nonce: %w", err)
	}
	now := time.Now().UTC()
	exp := now.Add(web3NonceTTL)
	uri := "http://" + domain
	const statement = "Sign in to TAC Ai Smart Chain with your wallet. This signature is for login only and costs no gas."
	message := domain + " wants you to sign in with your Ethereum account:\n" +
		addr + "\n\n" +
		statement + "\n\n" +
		"URI: " + uri + "\n" +
		"Version: 1\n" +
		"Chain ID: " + chainID + "\n" +
		"Nonce: " + nonce + "\n" +
		"Issued At: " + now.Format(time.RFC3339Nano) + "\n" +
		"Expiration Time: " + exp.Format(time.RFC3339Nano)

	a.web3NonceMu.Lock()
	if a.web3Nonces == nil {
		a.web3Nonces = map[string]web3Challenge{}
	}
	a.web3Nonces[nonce] = web3Challenge{Address: addr, Message: message, ExpiresAt: exp.Unix()}
	a.web3NonceMu.Unlock()
	return message, nonce, nil
}

// LoginWithWallet 以以太坊地址 find-or-create 會員。
// 合成 email：web3_<addr去掉0x並小寫>@web3.tac（滿足 UNIQUE NOT NULL）；
// password_hash 為不可經 email/密碼登入的佔位值。回傳 (uid, 合成或既有 email)。
func (a *AuthService) LoginWithWallet(address string) (int64, string, error) {
	addr := strings.ToLower(strings.TrimSpace(address))
	if !ethAddrRe.MatchString(addr) {
		return 0, "", fmt.Errorf("auth: 地址格式應為 0x + 40 hex")
	}
	var id int64
	var email string
	err := a.db.QueryRow(`SELECT id, email FROM users WHERE wallet_addr=? LIMIT 1`, addr).Scan(&id, &email)
	if err == nil {
		return id, email, nil
	}
	if err != sql.ErrNoRows {
		return 0, "", fmt.Errorf("auth: 查詢錢包使用者: %w", err)
	}
	// 相容：依合成郵箱再查一次（例如舊資料 wallet_addr 未回填）。
	synth := "web3_" + addr[2:] + "@web3.tac"
	err = a.db.QueryRow(`SELECT id, email FROM users WHERE email=? LIMIT 1`, synth).Scan(&id, &email)
	if err == nil {
		if _, uerr := a.db.Exec(`UPDATE users SET wallet_addr=? WHERE id=? AND wallet_addr=''`, addr, id); uerr != nil {
			return 0, "", fmt.Errorf("auth: 回填錢包地址: %w", uerr)
		}
		return id, email, nil
	}
	if err != sql.ErrNoRows {
		return 0, "", fmt.Errorf("auth: 查詢 web3 使用者: %w", err)
	}
	res, err := a.db.Exec(`INSERT INTO users(email, password_hash, created_at, wallet_addr, referrer) VALUES(?,?,?,?,?)`,
		synth, "web3$no_password", time.Now().Unix(), addr, "")
	if err != nil {
		return 0, "", fmt.Errorf("auth: 建立 web3 使用者: %w", err)
	}
	uid, err := res.LastInsertId()
	if err != nil {
		return 0, "", fmt.Errorf("auth: 取得 web3 使用者 id: %w", err)
	}
	return uid, synth, nil
}

// userByID 依主鍵取出會員基本資料。
func (a *AuthService) userByID(uid int64) (*AuthUser, error) {
	var email, wallet string
	var createdAt int64
	var isAdmin int
	err := a.db.QueryRow(`SELECT email, created_at, wallet_addr, is_admin FROM users WHERE id=?`, uid).
		Scan(&email, &createdAt, &wallet, &isAdmin)
	if err != nil {
		return nil, fmt.Errorf("auth: 查詢使用者: %w", err)
	}
	return &AuthUser{ID: uid, Email: email, CreatedAt: createdAt, WalletAddr: wallet, IsAdmin: isAdmin == 1}, nil
}

// VerifyWeb3Signature 驗證 EIP-191 簽名：
//  1. 從訊息取出 nonce，比對記憶體挑戰（存在、未用、未過期、地址相符）；
//  2. 立即作廢 nonce（一次性，防重放）；
//  3. EIP-191 雜湊後 recover 簽名者地址，須等於挑戰地址；
//  4. LoginWithWallet 建立/取得會員，SignSession 發 token。
//
// 回傳 (uid, token, user, err)。
func (a *AuthService) VerifyWeb3Signature(address, message, sigHex string) (int64, string, *AuthUser, error) {
	addr := strings.ToLower(strings.TrimSpace(address))
	if !ethAddrRe.MatchString(addr) {
		return 0, "", nil, fmt.Errorf("auth: 地址格式應為 0x + 40 hex")
	}
	nonce := ""
	for _, line := range strings.Split(message, "\n") {
		if strings.HasPrefix(line, "Nonce: ") {
			nonce = strings.TrimSpace(strings.TrimPrefix(line, "Nonce: "))
			break
		}
	}
	if nonce == "" {
		return 0, "", nil, fmt.Errorf("auth: 訊息缺少 nonce")
	}

	a.web3NonceMu.Lock()
	ch, ok := a.web3Nonces[nonce]
	if ok {
		delete(a.web3Nonces, nonce) // 一次性：無論成敗皆作廢
	}
	a.web3NonceMu.Unlock()
	if !ok {
		return 0, "", nil, fmt.Errorf("auth: nonce 不存在或已使用")
	}
	if time.Now().Unix() > ch.ExpiresAt {
		return 0, "", nil, fmt.Errorf("auth: 登入訊息已過期，請重新取得")
	}
	if !strings.EqualFold(ch.Address, addr) {
		return 0, "", nil, fmt.Errorf("auth: 地址與登入訊息不符")
	}

	raw := strings.TrimPrefix(strings.TrimPrefix(sigHex, "0x"), "0X")
	sig, err := hex.DecodeString(raw)
	if err != nil || len(sig) != 65 {
		return 0, "", nil, fmt.Errorf("auth: 簽名需為 65 bytes hex（personal_sign 輸出）")
	}
	hash := crypto.HashPersonalMessage([]byte(message))
	recovered, err := crypto.RecoverSignerAddressFromSig(hash, sig)
	if err != nil {
		return 0, "", nil, fmt.Errorf("auth: 簽名恢復失敗: %w", err)
	}
	if strings.ToLower(recovered) != strings.ToLower(ch.Address) {
		return 0, "", nil, fmt.Errorf("auth: 簽名者地址與錢包地址不符")
	}

	uid, email, err := a.LoginWithWallet(addr)
	if err != nil {
		return 0, "", nil, err
	}
	token, err := a.SignSession(uid, email)
	if err != nil {
		return 0, "", nil, err
	}
	user, err := a.userByID(uid)
	if err != nil {
		return 0, "", nil, err
	}
	return uid, token, user, nil
}
