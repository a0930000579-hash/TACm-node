// Package liquidstake 提供流動性質押（Liquid Staking，Lido 模式）——
// 用戶存入 TACm 獲得 stTACm 流動憑證（1:1 起，隨池內委託獎勵匯率上漲），
// 憑證可在 DeFi 使用；隨時可依現行匯率贖回 TACm。
// v73 與主流鏈同級：質押即流動、匯率制記帳、SQLite 持久化。
package liquidstake

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound 帳戶不存在。
var ErrNotFound = errors.New("liquidstake: account not found")

// ErrInsufficient 餘額不足。
var ErrInsufficient = errors.New("liquidstake: insufficient balance")

// Account 用戶流動性質押帳戶：質押本金與 stTACm 憑證餘額。
// 獎勵採匯率制：stTACm 餘額不變，兌換率隨池內獎勵上升，贖回值隨之增長。
type Account struct {
	Address string  // 用戶地址（0x…）
	Staked  float64 // 累計質押 TACm（本金，僅統計）
	StTACm  float64 // stTACm 流動憑證餘額
}

// Stats 全池統計。
type Stats struct {
	TotalStaked    float64 // 池內 TACm 總質押
	TotalStTACm    float64 // 流通 stTACm 總量
	ExchangeRate   float64 // 1 stTACm 可兌換 TACm（匯率）
	APY            float64 // 年化顯示（由節點委託收益更新，百分比）
	RewardReserve  float64 // 待分配獎勵儲備
	Depositors     int     // 存款帳戶數
	RedeemableTACm float64 // 可贖回 TACm 總額（= TotalStTACm × rate）
}

// RatePoint 單一匯率歷史取樣點（v76：後端持久化，非前端本機）。
type RatePoint struct {
	TS   int64   `json:"ts"`
	Rate float64 `json:"rate"`
}

// LiquidStake 流動性質押引擎。
type LiquidStake struct {
	db *sql.DB
	mu sync.Mutex
}

// New 開啟（或建立）流動性質押資料庫。
func New(path string) (*LiquidStake, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("liquidstake: mkdir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("liquidstake: open: %w", err)
	}
	ls := &LiquidStake{db: db}
	if err := ls.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return ls, nil
}

func (l *LiquidStake) init() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS ls_accounts (
			address TEXT PRIMARY KEY,
			staked REAL NOT NULL DEFAULT 0,
			sttacm REAL NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS ls_meta (
			key TEXT PRIMARY KEY,
			value REAL NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS ls_rate_history (
			ts INTEGER PRIMARY KEY,
			rate REAL NOT NULL
		)`,
	}
	for _, s := range stmts {
		if _, err := l.db.Exec(s); err != nil {
			return fmt.Errorf("liquidstake: init: %w", err)
		}
	}
	// 初始化元資料：總質押/憑證/獎勵/APY
	for k, v := range map[string]float64{
		"total_staked": 0, "total_sttacm": 0, "rewards": 0, "apy": 0,
	} {
		var cnt int
		if err := l.db.QueryRow("SELECT COUNT(*) FROM ls_meta WHERE key=?", k).Scan(&cnt); err != nil {
			return err
		}
		if cnt == 0 {
			if _, err := l.db.Exec("INSERT INTO ls_meta(key,value) VALUES(?,?)", k, v); err != nil {
				return err
			}
		}
	}
	return nil
}

func (l *LiquidStake) meta(key string) float64 {
	var v float64
	_ = l.db.QueryRow("SELECT value FROM ls_meta WHERE key=?", key).Scan(&v)
	return v
}

func (l *LiquidStake) setMeta(key string, v float64) error {
	_, err := l.db.Exec("UPDATE ls_meta SET value=? WHERE key=?", v, key)
	return err
}

// ExchangeRate 現行兌換率：池資產（質押+獎勵）÷ stTACm 總量；無憑證時 1.0。
// recordRate 寫入目前匯率歷史點（v76 後端持久化曲線）。
func (l *LiquidStake) recordRate() {
	rate := l.ExchangeRate()
	if rate <= 0 {
		return
	}
	now := time.Now().UnixMilli()
	if _, err := l.db.Exec(`INSERT INTO ls_rate_history(ts, rate) VALUES(?,?)`, now, rate); err != nil {
		return
	}
}

// RateHistory 回最近 N 個匯率歷史點（升序）。
func (l *LiquidStake) RateHistory(limit int) ([]RatePoint, error) {
	if limit <= 0 || limit > 1000 {
		limit = 180
	}
	rows, err := l.db.Query(`SELECT ts, rate FROM (SELECT ts, rate FROM ls_rate_history ORDER BY ts DESC LIMIT ?) ORDER BY ts ASC`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]RatePoint, 0, limit)
	for rows.Next() {
		var p RatePoint
		if err := rows.Scan(&p.TS, &p.Rate); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func (l *LiquidStake) ExchangeRate() float64 {
	ts, st, rw := l.meta("total_staked"), l.meta("total_sttacm"), l.meta("rewards")
	if st <= 0 {
		return 1.0
	}
	rate := (ts + rw) / st
	if rate <= 0 {
		return 1.0
	}
	return rate
}

// Deposit 存入 TACm，按現行匯率鑄造 stTACm 憑證（初期 1:1）。
func (l *LiquidStake) Deposit(address string, amount float64) (float64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if amount <= 0 {
		return 0, errors.New("liquidstake: amount must be positive")
	}
	rate := l.ExchangeRate()
	minted := amount / rate
	tx, err := l.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var staked, stt float64
	err = tx.QueryRow("SELECT staked, sttacm FROM ls_accounts WHERE address=?", address).Scan(&staked, &stt)
	if err != nil {
		if err != sql.ErrNoRows {
			return 0, err
		}
		if _, ierr := tx.Exec("INSERT INTO ls_accounts(address,staked,sttacm) VALUES(?,?,?)", address, amount, minted); ierr != nil {
			return 0, ierr
		}
	} else {
		if _, uerr := tx.Exec("UPDATE ls_accounts SET staked=?, sttacm=? WHERE address=?", staked+amount, stt+minted, address); uerr != nil {
			return 0, uerr
		}
	}
	if _, err = tx.Exec("UPDATE ls_meta SET value=value+? WHERE key='total_staked'", amount); err != nil {
		return 0, err
	}
	if _, err = tx.Exec("UPDATE ls_meta SET value=value+? WHERE key='total_sttacm'", minted); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	l.recordRate()
	log.Printf("[liquidstake] deposit address=%s amount=%.4f minted_stTACm=%.4f", address, amount, minted)
	return minted, nil
}

// Redeem 以 stTACm 憑證依現行匯率贖回 TACm。
func (l *LiquidStake) Redeem(address string, stTACm float64) (float64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if stTACm <= 0 {
		return 0, errors.New("liquidstake: amount must be positive")
	}
	rate := l.ExchangeRate()
	tx, err := l.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var staked, stt float64
	err = tx.QueryRow("SELECT staked, sttacm FROM ls_accounts WHERE address=?", address).Scan(&staked, &stt)
	if err == sql.ErrNoRows {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if stt < stTACm-1e-9 {
		return 0, ErrInsufficient
	}
	// 贖回值 = 憑證 × 匯率；本金統計隨贖回比例縮減
	out := stTACm * rate
	redeemStaked := 0.0
	if stt > 0 {
		redeemStaked = staked * (stTACm / stt)
	}
	if _, err = tx.Exec("UPDATE ls_accounts SET staked=?, sttacm=? WHERE address=?", staked-redeemStaked, stt-stTACm, address); err != nil {
		return 0, err
	}
	if _, err = tx.Exec("UPDATE ls_meta SET value=value-? WHERE key='total_staked'", redeemStaked); err != nil {
		return 0, err
	}
	if _, err = tx.Exec("UPDATE ls_meta SET value=value-? WHERE key='total_sttacm'", stTACm); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	l.recordRate()
	log.Printf("[liquidstake] redeem address=%s stTACm=%.4f -> TACm=%.4f", address, stTACm, out)
	return out, nil
}

// AddRewards 注入節點委託獎勵（匯率隨之上升；可由後續輪的委託收益回調驅動）。
func (l *LiquidStake) AddRewards(amount float64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if amount <= 0 {
		return errors.New("liquidstake: rewards must be positive")
	}
	if err := l.setMeta("rewards", l.meta("rewards")+amount); err != nil {
		return err
	}
	l.recordRate()
	return nil
}

// SetAPY 設定年化顯示（由節點委託實際收益更新）。
func (l *LiquidStake) SetAPY(apy float64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.setMeta("apy", apy)
}

// PoolShare v75：流動質押池在節點委託分潤中的份額——
// 池視為節點委託人，占比 = 池質押 / (節點自質押 + 池質押)。
// 由節點每塊調用並注入 AddRewards，匯率隨真實出塊獎勵增長。
func (l *LiquidStake) PoolShare(selfStake, nodeShare float64) float64 {
	st := l.meta("total_staked")
	if st <= 0 || selfStake <= 0 || nodeShare <= 0 {
		return 0
	}
	return nodeShare * st / (selfStake + st)
}

// Balance 查詢帳戶：本金、憑證、可贖回 TACm。
func (l *LiquidStake) Balance(address string) (Account, float64, error) {
	var a Account
	var staked, stt float64
	err := l.db.QueryRow("SELECT staked, sttacm FROM ls_accounts WHERE address=?", address).Scan(&staked, &stt)
	if err == sql.ErrNoRows {
		return a, 0, ErrNotFound
	}
	if err != nil {
		return a, 0, err
	}
	a = Account{Address: address, Staked: staked, StTACm: stt}
	return a, stt * l.ExchangeRate(), nil
}

// Stats 全池統計。
func (l *LiquidStake) Stats() Stats {
	ts, st, rw, apy := l.meta("total_staked"), l.meta("total_sttacm"), l.meta("rewards"), l.meta("apy")
	var n int
	_ = l.db.QueryRow("SELECT COUNT(*) FROM ls_accounts WHERE sttacm>0").Scan(&n)
	rate := l.ExchangeRate()
	return Stats{
		TotalStaked: ts, TotalStTACm: st, ExchangeRate: rate,
		APY: apy, RewardReserve: rw, Depositors: n,
		RedeemableTACm: st * rate,
	}
}

// Close 關閉資料庫。
func (l *LiquidStake) Close() error { return l.db.Close() }
