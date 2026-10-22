package ens

// ens.go — TAC 鏈上域名系統（對標 ENS）：.tac 名稱註冊／解析／反向解析。
// 名稱規範：全小寫、僅 [a-z0-9-]，最長 63 字元，主網尾綴 .tac（記錄不含尾綴）。
// namehash 依 ENS 標準（keccak 層疊）生成節點識別；記錄持久化於 SQLite（ens.db）。

import (
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"tacm/internal/crypto"
)

const (
	// MaxNameLen 單一 label 最長長度（對標 ENS 63 bytes）。
	MaxNameLen = 63
)

var nameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// Namehash 依 ENS 標準計算 namehash（每 label keccak 層疊）。
func Namehash(name string) string {
	node := make([]byte, 32)
	if name == "" {
		return "0x" + hex.EncodeToString(crypto.Keccak256(node))
	}
	labels := strings.Split(strings.ToLower(name), ".")
	for i := len(labels) - 1; i >= 0; i-- {
		labelHash := crypto.Keccak256([]byte(labels[i]))
		node = crypto.Keccak256(append(append(make([]byte, 0, 64), node...), labelHash...))
	}
	return "0x" + hex.EncodeToString(node)
}

// ValidName 檢查名稱合法性（.tac 尾綴不須傳入）。
func ValidName(name string) bool {
	if name == "" || len(name) > MaxNameLen {
		return false
	}
	return nameRe.MatchString(name)
}

// NameRecord 域名記錄。
type NameRecord struct {
	Name      string `json:"name"`
	Node      string `json:"node"` // namehash（0x...）
	Owner     string `json:"owner"`
	Target    string `json:"target"`
	ExpiresAt int64  `json:"expires_at"`
	CreatedAt int64  `json:"created_at"`
}

// Store ENS 記錄儲存。
type Store struct {
	db *sql.DB
}

// Open 開啟 ens.db（WAL、單連線）並建表。
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("ens: 建立數據目錄: %w", err)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)",
		filepath.Join(dataDir, "ens.db"))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("ens: 開啟數據庫: %w", err)
	}
	db.SetMaxOpenConns(1)
	schema := `
CREATE TABLE IF NOT EXISTS ens_names (
  name TEXT PRIMARY KEY,
  node TEXT NOT NULL,
  owner TEXT NOT NULL,
  target TEXT NOT NULL DEFAULT '',
  expires_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ens_owner ON ens_names(owner);
CREATE INDEX IF NOT EXISTS idx_ens_node ON ens_names(node);
`
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ens: 初始化 schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close 關閉資料庫。
func (s *Store) Close() error { return s.db.Close() }

// Register 註冊／續期名稱（付費由節點層處理；此處只寫記錄）。
// 已存在且未過期 → 續期（累加年限）；已存在但過期 → 重新註冊（重置 owner）。
func (s *Store) Register(name, owner, target string, years int64) (*NameRecord, error) {
	if !ValidName(name) {
		return nil, fmt.Errorf("名稱不合法（僅 [a-z0-9-]，≤63 字元）")
	}
	if years < 1 {
		return nil, fmt.Errorf("註冊年限至少 1 年")
	}
	now := time.Now().Unix()
	node := Namehash(name + ".tac")
	var old TargetRecord
	row := s.db.QueryRow(`SELECT owner, target, expires_at FROM ens_names WHERE name=?`, name)
	err := row.Scan(&old.Owner, &old.Target, &old.ExpiresAt)
	if err == sql.ErrNoRows {
		// 全新註冊。
		exp := now + years*365*24*3600
		if _, err := s.db.Exec(`INSERT INTO ens_names(name, node, owner, target, expires_at, created_at) VALUES(?,?,?,?,?,?)`,
			name, node, owner, target, exp, now); err != nil {
			return nil, fmt.Errorf("ens: 註冊: %w", err)
		}
		return &NameRecord{Name: name, Node: node, Owner: owner, Target: target, ExpiresAt: exp, CreatedAt: now}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("ens: 查詢現有名稱: %w", err)
	}
	if old.ExpiresAt > now && old.Owner != owner {
		return nil, fmt.Errorf("名稱已被註冊（過期前不可轉移）")
	}
	// 續期：基準＝max(now, 未過期原到期)，加 years 年。
	base := now
	if old.ExpiresAt > now {
		base = old.ExpiresAt
	}
	exp := base + years*365*24*3600
	if old.ExpiresAt <= now {
		// 過期重新註冊：重置 owner/target。
		if _, err := s.db.Exec(`UPDATE ens_names SET node=?, owner=?, target=?, expires_at=?, created_at=? WHERE name=?`,
			node, owner, target, exp, now, name); err != nil {
			return nil, fmt.Errorf("ens: 重新註冊: %w", err)
		}
		return &NameRecord{Name: name, Node: node, Owner: owner, Target: target, ExpiresAt: exp, CreatedAt: now}, nil
	}
	if target != "" {
		if _, err := s.db.Exec(`UPDATE ens_names SET target=?, expires_at=? WHERE name=?`, target, exp, name); err != nil {
			return nil, fmt.Errorf("ens: 續期: %w", err)
		}
	} else {
		if _, err := s.db.Exec(`UPDATE ens_names SET expires_at=? WHERE name=?`, exp, name); err != nil {
			return nil, fmt.Errorf("ens: 續期: %w", err)
		}
	}
	return &NameRecord{Name: name, Node: node, Owner: owner, Target: old.Target, ExpiresAt: exp, CreatedAt: now}, nil
}

// TargetRecord 內部查詢用（owner/target/expires）。
type TargetRecord struct {
	Owner     string
	Target    string
	ExpiresAt int64
}

// Resolve 名稱→目標地址（過期視為未註冊）。
func (s *Store) Resolve(name string) (TargetRecord, error) {
	if !ValidName(name) {
		return TargetRecord{}, fmt.Errorf("名稱不合法")
	}
	var rec TargetRecord
	err := s.db.QueryRow(`SELECT owner, target, expires_at FROM ens_names WHERE name=?`, name).
		Scan(&rec.Owner, &rec.Target, &rec.ExpiresAt)
	if err == sql.ErrNoRows {
		return TargetRecord{}, fmt.Errorf("名稱未註冊")
	}
	if err != nil {
		return TargetRecord{}, fmt.Errorf("ens: 解析: %w", err)
	}
	if rec.ExpiresAt <= time.Now().Unix() {
		return TargetRecord{}, fmt.Errorf("名稱已過期")
	}
	return rec, nil
}

// Reverse 地址→名稱（該地址名下最新註冊、未過期者；多筆時取最早註冊者）。
func (s *Store) Reverse(address string) (string, error) {
	now := time.Now().Unix()
	var name string
	err := s.db.QueryRow(`SELECT name FROM ens_names WHERE owner=? AND expires_at>? ORDER BY created_at ASC LIMIT 1`, address, now).
		Scan(&name)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("該地址無域名")
	}
	if err != nil {
		return "", fmt.Errorf("ens: 反向解析: %w", err)
	}
	return name, nil
}

// My 列出某地址持有（未過期）的名稱。
func (s *Store) My(address string) ([]NameRecord, error) {
	now := time.Now().Unix()
	rows, err := s.db.Query(`SELECT name, node, owner, target, expires_at, created_at FROM ens_names WHERE owner=? AND expires_at>? ORDER BY created_at DESC LIMIT 100`, address, now)
	if err != nil {
		return nil, fmt.Errorf("ens: 查詢名下域名: %w", err)
	}
	defer rows.Close()
	out := make([]NameRecord, 0, 8)
	for rows.Next() {
		var r NameRecord
		if err := rows.Scan(&r.Name, &r.Node, &r.Owner, &r.Target, &r.ExpiresAt, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
