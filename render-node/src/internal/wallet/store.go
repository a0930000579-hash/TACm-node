package wallet

import (
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Store 為錢包帳本（多資產餘額 + 審計分錄 + TiUSD 供給）的 SQLite 持久化。
// 與鏈存儲一致：單連接（MaxOpenConns=1）、參數化查詢、寫入走事務。
// TACm 餘額以十進制字符串存 TEXT（big.Int 精度，無溢出），穩定幣存 INTEGER。
type Store struct {
	db *sql.DB
}

const walletSchema = `
CREATE TABLE IF NOT EXISTS wallet_accounts (
    address      TEXT PRIMARY KEY,
    tacm_balance TEXT NOT NULL DEFAULT '0',    -- big.Int 十進制（1e18 單位）
    tiusd_balance INTEGER NOT NULL DEFAULT 0,  -- 1e6 最小單位
    usdt_balance  INTEGER NOT NULL DEFAULT 0,  -- 1e6 最小單位
    created_ts   INTEGER NOT NULL,
    updated_ts   INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS wallet_ledger (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    ts      INTEGER NOT NULL,
    kind    TEXT NOT NULL,
    asset   TEXT NOT NULL,
    account TEXT NOT NULL,
    delta   TEXT NOT NULL,
    memo    TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS tiusd_supply (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    supply        INTEGER NOT NULL DEFAULT 0,
    total_minted  INTEGER NOT NULL DEFAULT 0,
    total_burned  INTEGER NOT NULL DEFAULT 0,
    updated_ts    INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS wallet_sync (
    id     INTEGER PRIMARY KEY CHECK (id = 1),
    height INTEGER NOT NULL DEFAULT 0,
    ts     INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS chain_stats (
    id                INTEGER PRIMARY KEY CHECK (id = 1),
    total_mined_tacm  TEXT NOT NULL DEFAULT '0',
    updated_ts        INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ledger_ts ON wallet_ledger(ts DESC);
CREATE INDEX IF NOT EXISTS idx_ledger_account ON wallet_ledger(account);
`

// Open 打開（或創建）錢包帳本庫。
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("wallet: 建立數據目錄: %w", err)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)",
		filepath.Join(dataDir, "wallet.db"))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("wallet: 開啟數據庫: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(walletSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("wallet: 初始化 schema: %w", err)
	}
	if _, err := db.Exec(minerSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("wallet: 初始化礦機 schema: %w", err)
	}
	if _, err := db.Exec("INSERT OR IGNORE INTO tiusd_supply (id, supply, total_minted, total_burned, updated_ts) VALUES (1, 0, 0, 0, ?)",
		time.Now().Unix()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("wallet: 初始化供給: %w", err)
	}
	return &Store{db: db}, nil
}

// Close 關閉帳本。
// Accounts 返回全部錢包帳戶（快照導出用）。
func (s *Store) Accounts() ([]Account, error) {
	rows, err := s.db.Query(`SELECT address, tacm_balance, tiusd_balance, usdt_balance, created_ts, updated_ts FROM wallet_accounts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var a Account
		var tacm string
		if err := rows.Scan(&a.Address, &tacm, &a.TiUSDBalance, &a.USDTBalance, &a.CreatedTs, &a.UpdatedTs); err != nil {
			return nil, err
		}
		bi, ok := new(big.Int).SetString(tacm, 10)
		if !ok {
			bi = new(big.Int)
		}
		a.TACmBalance = bi
		out = append(out, a)
	}
	return out, rows.Err()
}

// ImportAccounts 批量導入/覆寫帳戶餘額（快照導入用）。
func (s *Store) ImportAccounts(accs []Account) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, a := range accs {
		tacm := "0"
		if a.TACmBalance != nil {
			tacm = a.TACmBalance.String()
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO wallet_accounts
			(address, tacm_balance, tiusd_balance, usdt_balance, created_ts, updated_ts)
			VALUES (?, ?, ?, ?, ?, ?)`,
			a.Address, tacm, a.TiUSDBalance, a.USDTBalance, a.CreatedTs, a.UpdatedTs); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Close() error { return s.db.Close() }

// Account 為一個錢包帳戶的資產快照。
type Account struct {
	Address      string   `json:"address"`
	TACmBalance  *big.Int `json:"tacm_balance"`
	TiUSDBalance int64    `json:"tiusd_balance"`
	USDTBalance  int64    `json:"usdt_balance"`
	CreatedTs    int64    `json:"created_ts"`
	UpdatedTs    int64    `json:"updated_ts"`
}

// Balance 返回帳戶快照（不存在時返回零餘額帳戶，不報錯）。
func (s *Store) Balance(address string) (*Account, error) {
	row := s.db.QueryRow(
		`SELECT address, tacm_balance, tiusd_balance, usdt_balance, created_ts, updated_ts
		 FROM wallet_accounts WHERE address = ?`, address)
	a := &Account{TACmBalance: new(big.Int)}
	var tacm string
	err := row.Scan(&a.Address, &tacm, &a.TiUSDBalance, &a.USDTBalance, &a.CreatedTs, &a.UpdatedTs)
	if err == sql.ErrNoRows {
		return &Account{Address: address, TACmBalance: new(big.Int), CreatedTs: time.Now().Unix(), UpdatedTs: time.Now().Unix()}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("wallet: 讀取餘額 %s: %w", address, err)
	}
	if _, ok := a.TACmBalance.SetString(tacm, 10); !ok {
		return nil, fmt.Errorf("wallet: 餘額非法 %s: %q", address, tacm)
	}
	return a, nil
}

// ApplyEntries 在單一事務中應用一組分錄：更新帳戶餘額 + 寫審計流。
// 前置條件：餘額充足、分錄守恆（checkInvariant）；任一步失敗全回滾。
func (s *Store) ApplyEntries(entries []LedgerEntry) error {
	if err := checkInvariant(entries); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("wallet: 開啟事務: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	ts := time.Now().Unix()
	for _, e := range entries {
		if e.Delta == "" || e.Delta == "0" {
			continue
		}
		switch e.Asset {
		case AssetTACm:
			if err := applyBigDelta(tx, e, ts); err != nil {
				return err
			}
		case AssetTiUSD, AssetUSDT:
			if err := applyIntDelta(tx, e, ts); err != nil {
				return err
			}
		default:
			return fmt.Errorf("wallet: 不支持的資產 %s", e.Asset)
		}
	}
	for _, e := range entries {
		if e.Delta == "" || e.Delta == "0" {
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO wallet_ledger (ts, kind, asset, account, delta, memo) VALUES (?, ?, ?, ?, ?, ?)`,
			ts, string(e.Kind), string(e.Asset), e.Account, e.Delta, e.Memo); err != nil {
			return fmt.Errorf("wallet: 寫分錄: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("wallet: 提交事務: %w", err)
	}
	return nil
}

// applyBigDelta 更新 TACm（big.Int TEXT）餘額：讀現值 → 計算 → 檢查 ≥0 → 寫回。
func applyBigDelta(tx *sql.Tx, e LedgerEntry, ts int64) error {
	delta, ok := new(big.Int).SetString(e.Delta, 10)
	if !ok {
		return fmt.Errorf("wallet: 金額非法 %q", e.Delta)
	}
	var cur string
	err := tx.QueryRow(`SELECT tacm_balance FROM wallet_accounts WHERE address = ?`, e.Account).Scan(&cur)
	if err == sql.ErrNoRows {
		cur = "0"
		if delta.Sign() < 0 {
			return fmt.Errorf("wallet: 餘額不足 %s TACm (Δ%s)", e.Account, e.Delta)
		}
		if _, err := tx.Exec(
			`INSERT INTO wallet_accounts (address, tacm_balance, tiusd_balance, usdt_balance, created_ts, updated_ts)
			 VALUES (?, '0', 0, 0, ?, ?)`, e.Account, ts, ts); err != nil {
			return fmt.Errorf("wallet: 建帳戶 %s: %w", e.Account, err)
		}
	} else if err != nil {
		return fmt.Errorf("wallet: 讀餘額 %s: %w", e.Account, err)
	}
	bal, ok := new(big.Int).SetString(cur, 10)
	if !ok {
		return fmt.Errorf("wallet: 餘額非法 %q", cur)
	}
	bal.Add(bal, delta)
	if bal.Sign() < 0 {
		return fmt.Errorf("wallet: 餘額不足 %s TACm (Δ%s, 餘額 %s)", e.Account, e.Delta, cur)
	}
	if _, err := tx.Exec(
		`UPDATE wallet_accounts SET tacm_balance = ?, updated_ts = ? WHERE address = ?`,
		bal.String(), ts, e.Account); err != nil {
		return fmt.Errorf("wallet: 更新餘額 %s: %w", e.Account, err)
	}
	return nil
}

// applyIntDelta 更新穩定幣（INTEGER）餘額：原子條件更新（餘額不足影響 0 行）。
func applyIntDelta(tx *sql.Tx, e LedgerEntry, ts int64) error {
	col, err := balanceColumn(e.Asset)
	if err != nil {
		return err
	}
	q := fmt.Sprintf(
		`UPDATE wallet_accounts SET %s = %s + ?, updated_ts = ?
		 WHERE address = ? AND %s + ? >= 0`, col, col, col)
	res, err := tx.Exec(q, e.Delta, ts, e.Account, e.Delta)
	if err != nil {
		return fmt.Errorf("wallet: 更新餘額 %s/%s: %w", e.Account, e.Asset, err)
	}
	aff, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if aff == 0 {
		var bal int64
		_ = tx.QueryRow(fmt.Sprintf(`SELECT %s FROM wallet_accounts WHERE address = ?`, col), e.Account).Scan(&bal)
		if e.Delta[0] == '-' {
			return fmt.Errorf("wallet: 餘額不足 %s %s (Δ%s, 餘額 %d)", e.Account, e.Asset, e.Delta, bal)
		}
		if _, err := tx.Exec(
			`INSERT INTO wallet_accounts (address, tacm_balance, tiusd_balance, usdt_balance, created_ts, updated_ts)
			 VALUES (?, '0', 0, 0, ?, ?)`, e.Account, ts, ts); err != nil {
			return fmt.Errorf("wallet: 建帳戶 %s: %w", e.Account, err)
		}
		if _, err := tx.Exec(q, e.Delta, ts, e.Account, e.Delta); err != nil {
			return fmt.Errorf("wallet: 重試更新餘額 %s: %w", e.Account, err)
		}
	}
	return nil
}

// TiUSDSupply 為穩定幣供給摘要。
type TiUSDSupply struct {
	Supply      int64 `json:"supply"`
	TotalMinted int64 `json:"total_minted"`
	TotalBurned int64 `json:"total_burned"`
	UpdatedTs   int64 `json:"updated_ts"`
}

// TiUSDSummary 返回供給摘要（未初始化返回零）。
func (s *Store) TiUSDSummary() (*TiUSDSupply, error) {
	row := s.db.QueryRow(`SELECT supply, total_minted, total_burned, updated_ts FROM tiusd_supply WHERE id = 1`)
	sup := &TiUSDSupply{}
	if err := row.Scan(&sup.Supply, &sup.TotalMinted, &sup.TotalBurned, &sup.UpdatedTs); err != nil {
		return nil, fmt.Errorf("wallet: 讀取供給: %w", err)
	}
	return sup, nil
}

// adjustTiUSD 以事務調整供給（mint 加、burn 減；不得低於 0）。
func (s *Store) adjustTiUSD(delta int64, totalMinted int64, totalBurned int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("wallet: 開啟事務: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.Exec(
		`UPDATE tiusd_supply SET supply = supply + ?, total_minted = total_minted + ?, total_burned = total_burned + ?, updated_ts = ?
		 WHERE id = 1 AND supply + ? >= 0`,
		delta, totalMinted, totalBurned, time.Now().Unix(), delta)
	if err != nil {
		return fmt.Errorf("wallet: 調整供給: %w", err)
	}
	aff, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if aff == 0 {
		return fmt.Errorf("wallet: TiUSD 供給不足無法銷毀")
	}
	return tx.Commit()
}

// Ledger 返回最近 n 條分錄（審計流，倒序）。
func (s *Store) Ledger(limit int) ([]LedgerEntry, error) {
	if limit <= 0 {
		limit = 50
	} else if limit > 5000 {
		limit = 5000
	}
	rows, err := s.db.Query(
		`SELECT id, ts, kind, asset, account, delta, memo FROM wallet_ledger ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("wallet: 讀分錄: %w", err)
	}
	defer rows.Close()
	out := make([]LedgerEntry, 0, limit)
	for rows.Next() {
		var e LedgerEntry
		var kind, asset, delta, memo string
		if err := rows.Scan(&e.ID, &e.Ts, &kind, &asset, &e.Account, &delta, &memo); err != nil {
			return nil, fmt.Errorf("wallet: 掃分錄: %w", err)
		}
		e.Kind, e.Asset, e.Delta, e.Memo = LedgerKind(kind), Asset(asset), delta, memo
		out = append(out, e)
	}
	return out, rows.Err()
}

func balanceColumn(a Asset) (string, error) {
	switch a {
	case AssetTACm:
		return "tacm_balance", nil
	case AssetTiUSD:
		return "tiusd_balance", nil
	case AssetUSDT:
		return "usdt_balance", nil
	default:
		return "", fmt.Errorf("wallet: 不支持的資產 %s", a)
	}
}

// ChainMinedTacm 讀取全鏈累計出塊產出（O(1)，不掃 ledger）。
func (s *Store) ChainMinedTacm() (*big.Int, error) {
	var v string
	err := s.db.QueryRow(`SELECT total_mined_tacm FROM chain_stats WHERE id=1`).Scan(&v)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return big.NewInt(0), nil
		}
		return nil, fmt.Errorf("wallet: 讀全鏈產出: %w", err)
	}
	out, ok := new(big.Int).SetString(v, 10)
	if !ok {
		return big.NewInt(0), nil
	}
	return out, nil
}

// AddMinedTacm 累加全鏈出塊產出（coinbase 分發時呼叫）。
func (s *Store) AddMinedTacm(delta *big.Int) error {
	if delta == nil || delta.Sign() <= 0 {
		return nil
	}
	cur, err := s.ChainMinedTacm()
	if err != nil {
		return err
	}
	cur.Add(cur, delta)
	_, err = s.db.Exec(`INSERT INTO chain_stats (id, total_mined_tacm, updated_ts) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET total_mined_tacm = excluded.total_mined_tacm, updated_ts = excluded.updated_ts`,
		cur.String(), time.Now().Unix())
	if err != nil {
		return fmt.Errorf("wallet: 累加全鏈產出: %w", err)
	}
	return nil
}

// SetMinedTacm 直接設全鏈出塊產出（M103 chain_stats 自癒：審計重放後修正計數器）。
func (s *Store) SetMinedTacm(total *big.Int) error {
	if total == nil || total.Sign() < 0 {
		return fmt.Errorf("wallet: 無效的全鏈產出")
	}
	_, err := s.db.Exec(`INSERT INTO chain_stats (id, total_mined_tacm, updated_ts) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET total_mined_tacm = excluded.total_mined_tacm, updated_ts = excluded.updated_ts`,
		total.String(), time.Now().Unix())
	if err != nil {
		return fmt.Errorf("wallet: 設全鏈產出: %w", err)
	}
	return nil
}
