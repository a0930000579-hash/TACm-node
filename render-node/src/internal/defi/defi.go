// Package defi 提供 TAC Ai 智能鏈 DeFi 模組（照 Python 原版搬運＋真實資產進出）：
//   - 流動性挖礦（pools / add / remove / claim reward，LP=sqrt 公式、APR 每日獎勵）
//   - 借貸市場（deposit / withdraw / borrow / repay，抵押率與 10% 年息）
//
// 資產流動全部對接 wallet 帳本：用戶入池走 Transfer（鏈上費 2% 入資金池）、
// 獎勵與借款發放走 Reward（鑄造）、池出資金走 InternalTransfer（免二次費）。
package defi

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// 系統帳戶（wallet ledger 用）。
const (
	LendingAccount = "defi_lending" // 借貸市場資金池帳戶
	IDOAccount     = "defi_ido"     // IDO 認購資金池帳戶
	RewardAccount  = "defi_rewards" // 流動性獎勵結算帳戶（預留）
	annualInterest = 0.10           // 借貸年利率（照 Python）
	poolPrefix     = "defi_lp_"     // 流動性池帳戶前綴（defi_lp_{pool_id}）
)

// Store DeFi 資料庫（SQLite）。
type Store struct {
	db             *sql.DB
	CompoundMinSec int64 // Vault 復投最小間隔（秒，預設 3600；測試可用 env TAC_VAULT_COMPOUND_MIN_SEC 覆蓋）
}

// Pool 流動性池視圖。
type Pool struct {
	ID           int64   `json:"id"`
	Pair         string  `json:"pair"`
	Token0       string  `json:"token0"`
	Token1       string  `json:"token1"`
	Reserve0     float64 `json:"reserve0"`
	Reserve1     float64 `json:"reserve1"`
	TotalLP      float64 `json:"total_lp"`
	RewardToken  string  `json:"reward_token"`
	RewardPerDay float64 `json:"reward_per_day"`
	APR          float64 `json:"apr"`
	CreatedAt    int64   `json:"created_at"`
}

// LPPosition 用戶流動性頭寸視圖。
type LPPosition struct {
	ID            int64   `json:"id"`
	Address       string  `json:"address"`
	PoolID        int64   `json:"pool_id"`
	LPAmount      float64 `json:"lp_amount"`
	Token0Amount  float64 `json:"token0_amount"`
	Token1Amount  float64 `json:"token1_amount"`
	LastRewardAt  int64   `json:"last_reward_at"`
	CreatedAt     int64   `json:"created_at"`
	Pair          string  `json:"pair"`
	Token0        string  `json:"token0"`
	Token1        string  `json:"token1"`
	Reserve0      float64 `json:"reserve0"`
	Reserve1      float64 `json:"reserve1"`
	TotalLP       float64 `json:"total_lp"`
	APR           float64 `json:"apr"`
	PendingReward float64 `json:"pending_reward"` // 預估待領獎勵
}

// LendingMarket 借貸市場視圖。
type LendingMarket struct {
	ID                   int64   `json:"id"`
	Asset                string  `json:"asset"`
	TotalDeposit         float64 `json:"total_deposit"`
	TotalBorrow          float64 `json:"total_borrow"`
	DepositAPR           float64 `json:"deposit_apr"`
	BorrowAPR            float64 `json:"borrow_apr"`
	CollateralFactor     float64 `json:"collateral_factor"`
	LiquidationThreshold float64 `json:"liquidation_threshold"`
	Enabled              bool    `json:"enabled"`
}

// LendingDeposit 用戶存款視圖。
type LendingDeposit struct {
	ID         int64   `json:"id"`
	Address    string  `json:"address"`
	Asset      string  `json:"asset"`
	Amount     float64 `json:"amount"`
	LastUpdate int64   `json:"last_update"`
	CreatedAt  int64   `json:"created_at"`
}

// Loan 用戶貸款視圖。
type Loan struct {
	ID               int64   `json:"id"`
	Address          string  `json:"address"`
	CollateralAsset  string  `json:"collateral_asset"`
	CollateralAmount float64 `json:"collateral_amount"`
	BorrowAsset      string  `json:"borrow_asset"`
	BorrowAmount     float64 `json:"borrow_amount"`
	CollateralValue  float64 `json:"collateral_value"`
	LoanValue        float64 `json:"loan_value"`
	LTV              float64 `json:"ltv"`
	LiquidationPrice float64 `json:"liquidation_price"`
	InterestAccrued  float64 `json:"interest_accrued"`
	Status           string  `json:"status"`
	LastUpdate       int64   `json:"last_update"`
	CreatedAt        int64   `json:"created_at"`
	InterestNow      float64 `json:"interest_now"` // 截至查詢的應計利息
}

const defiSchema = `
CREATE TABLE IF NOT EXISTS defi_pools (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  pair TEXT NOT NULL,
  token0 TEXT NOT NULL,
  token1 TEXT NOT NULL,
  reserve0 REAL NOT NULL DEFAULT 0,
  reserve1 REAL NOT NULL DEFAULT 0,
  total_lp REAL NOT NULL DEFAULT 0,
  reward_token TEXT NOT NULL DEFAULT 'TACm',
  reward_per_day REAL NOT NULL DEFAULT 0,
  apr REAL NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS defi_lp_positions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  address TEXT NOT NULL,
  pool_id INTEGER NOT NULL,
  lp_amount REAL NOT NULL DEFAULT 0,
  token0_amount REAL NOT NULL DEFAULT 0,
  token1_amount REAL NOT NULL DEFAULT 0,
  last_reward_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_lp_addr ON defi_lp_positions(address, pool_id);
CREATE TABLE IF NOT EXISTS defi_lending_markets (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  asset TEXT NOT NULL,
  total_deposit REAL NOT NULL DEFAULT 0,
  total_borrow REAL NOT NULL DEFAULT 0,
  deposit_apr REAL NOT NULL DEFAULT 0,
  borrow_apr REAL NOT NULL DEFAULT 0,
  collateral_factor REAL NOT NULL DEFAULT 0,
  liquidation_threshold REAL NOT NULL DEFAULT 0,
  enabled INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS defi_deposits (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  address TEXT NOT NULL,
  asset TEXT NOT NULL,
  amount REAL NOT NULL DEFAULT 0,
  last_update INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_dep_addr ON defi_deposits(address, asset);
CREATE TABLE IF NOT EXISTS defi_loans (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  address TEXT NOT NULL,
  collateral_asset TEXT NOT NULL,
  collateral_amount REAL NOT NULL DEFAULT 0,
  borrow_asset TEXT NOT NULL,
  borrow_amount REAL NOT NULL DEFAULT 0,
  collateral_value REAL NOT NULL DEFAULT 0,
  loan_value REAL NOT NULL DEFAULT 0,
  ltv REAL NOT NULL DEFAULT 0,
  liquidation_price REAL NOT NULL DEFAULT 0,
  interest_accrued REAL NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'active',
  last_update INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_loans_addr ON defi_loans(address, status);
CREATE TABLE IF NOT EXISTS defi_ido_projects (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  symbol TEXT NOT NULL,
  token_price REAL NOT NULL DEFAULT 0,
  raise_target REAL NOT NULL DEFAULT 0,
  raise_asset TEXT NOT NULL DEFAULT 'USDT',
  min_buy REAL NOT NULL DEFAULT 0,
  max_buy REAL NOT NULL DEFAULT 0,
  start_time INTEGER NOT NULL,
  end_time INTEGER NOT NULL,
  status TEXT NOT NULL DEFAULT 'upcoming',
  description TEXT,
  total_raised REAL NOT NULL DEFAULT 0,
  total_participants INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS defi_ido_subscriptions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  address TEXT NOT NULL,
  project_id INTEGER NOT NULL,
  subscribe_amount REAL NOT NULL DEFAULT 0,
  allocated_tokens REAL NOT NULL DEFAULT 0,
  claimed INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ido_addr ON defi_ido_subscriptions(address, project_id);
CREATE TABLE IF NOT EXISTS defi_vaults (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  strategy TEXT NOT NULL,
  underlying_asset TEXT NOT NULL,
  total_assets REAL NOT NULL DEFAULT 0,
  total_shares REAL NOT NULL DEFAULT 0,
  apr REAL NOT NULL DEFAULT 0,
  min_deposit REAL NOT NULL DEFAULT 0,
  performance_fee REAL NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'active',
  description TEXT,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS defi_vault_positions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  address TEXT NOT NULL,
  vault_id INTEGER NOT NULL,
  shares REAL NOT NULL DEFAULT 0,
  underlying_amount REAL NOT NULL DEFAULT 0,
  last_compound INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_vault_addr ON defi_vault_positions(address, vault_id);
`

// Open 開啟 DeFi 資料庫並種子預設池/市場（照 Python seed）。
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("defi: 建立數據目錄: %w", err)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)",
		filepath.Join(dataDir, "defi.db"))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("defi: 開啟數據庫: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(defiSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("defi: 初始化 schema: %w", err)
	}
	cm := int64(3600)
	if v := os.Getenv("TAC_VAULT_COMPOUND_MIN_SEC"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			cm = n
		}
	}
	st := &Store{db: db, CompoundMinSec: cm}
	if err := st.seedDefault(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return st, nil
}

// Close 關閉 DeFi 資料庫。
func (s *Store) Close() error { return s.db.Close() }

// seedDefault 種子預設流動性池與借貸市場（照 Python _seed_default_data）。
func (s *Store) seedDefault() error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM defi_pools`).Scan(&n); err != nil {
		return fmt.Errorf("defi: 檢查種子: %w", err)
	}
	if n > 0 {
		return nil
	}
	now := time.Now().Unix()
	pools := [][9]float64{
		{50000, 500000, 15811.39, 500, 0.45, 0, 0, 0, 0}, // TACM/USDT
		{30000, 300000, 9486.83, 300, 0.38, 0, 0, 0, 0},  // TACM/TiUSD
		{100000, 99500, 9974.97, 100, 0.12, 0, 0, 0, 0},  // TiUSD/USDT
	}
	pairNames := []string{"TACM/USDT", "TACM/TIUSD", "TIUSD/USDT"}
	tokens := [][2]string{{"TACm", "USDT"}, {"TACm", "TIUSD"}, {"TIUSD", "USDT"}}
	for i, p := range pools {
		if _, err := s.db.Exec(`INSERT INTO defi_pools(pair, token0, token1, reserve0, reserve1, total_lp, reward_token, reward_per_day, apr, created_at)
			VALUES(?,?,?,?,?,?,?,?,?,?)`, pairNames[i], tokens[i][0], tokens[i][1], p[0], p[1], p[2], "TACm", p[3], p[4], now); err != nil {
			return fmt.Errorf("defi: 種子池: %w", err)
		}
	}
	markets := [][7]float64{
		{500000, 150000, 0.06, 0.12, 0.75, 0.85, 1},
		{200000, 50000, 0.08, 0.15, 0.65, 0.75, 1},
		{300000, 80000, 0.05, 0.10, 0.80, 0.90, 1},
	}
	marketAssets := []string{"USDT", "TACm", "TIUSD"}
	for i, m := range markets {
		if _, err := s.db.Exec(`INSERT INTO defi_lending_markets(asset, total_deposit, total_borrow, deposit_apr, borrow_apr, collateral_factor, liquidation_threshold, enabled)
			VALUES(?,?,?,?,?,?,?,?)`, marketAssets[i], m[0], m[1], m[2], m[3], m[4], m[5], int(m[6])); err != nil {
			return fmt.Errorf("defi: 種子市場: %w", err)
		}
	}
	// 默認 IDO 項目（照 Python：TACm Governance / TACm AI）。
	// 時間窗起點可用 env TAC_IDO_START_OFFSET_SEC 覆蓋（測試用；預設 +1 天、結束 +3 天）。
	startOff := int64(86400)
	if v := os.Getenv("TAC_IDO_START_OFFSET_SEC"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			startOff = n
		}
	}
	day := int64(86400)
	projects := []struct {
		name, symbol, asset, desc     string
		price, target, minBuy, maxBuy float64
		startIn, endIn                int64
	}{
		{"TACm Governance", "TACG", "USDT", "TACm 生態治理代幣，參與社區投票和提案", 0.5, 50000, 50, 5000, startOff, startOff + day*2},
		{"TACm AI", "TACA", "USDT", "TACm AI 智能合約和預言機服務代幣", 1.0, 100000, 100, 10000, startOff + day*6, startOff + day*9},
	}
	for _, pj := range projects {
		if _, err := s.db.Exec(`INSERT INTO defi_ido_projects(name, symbol, token_price, raise_target, raise_asset, min_buy, max_buy, start_time, end_time, status, description, created_at)
			VALUES(?,?,?,?,?,?,?,?,?,'upcoming',?,?)`,
			pj.name, pj.symbol, pj.price, pj.target, pj.asset, pj.minBuy, pj.maxBuy, now+pj.startIn, now+pj.endIn, pj.desc, now); err != nil {
			return fmt.Errorf("defi: 種子 IDO: %w", err)
		}
	}
	// 默認收益聚合器 Vault（照 Python：TACM 穩健收益 / USDT 高收益 / TiUSD 活利寶）。
	vaults := []struct {
		name, strategy, asset, desc string
		total, apr, minDep, fee     float64
	}{
		{"TACM 穩健收益", "stable_farming", "TACm", "自動復投流動性挖礦收益，穩健型策略", 100000, 0.18, 100, 0.1},
		{"USDT 高收益", "yield_optimization", "USDT", "優化借貸和流動性收益，自動調配資金", 500000, 0.12, 50, 0.1},
		{"TiUSD 活利寶", "flexible_saving", "TIUSD", "靈活存取，每日結息，低風險穩定收益", 200000, 0.08, 10, 0.05},
	}
	for _, v := range vaults {
		if _, err := s.db.Exec(`INSERT INTO defi_vaults(name, strategy, underlying_asset, total_assets, total_shares, apr, min_deposit, performance_fee, status, description, created_at)
			VALUES(?,?,?,?,?,?,?,?,'active',?,?)`,
			v.name, v.strategy, v.asset, v.total, v.total, v.apr, v.minDep, v.fee, v.desc, now); err != nil {
			return fmt.Errorf("defi: 種子 Vault: %w", err)
		}
	}
	return nil
}

// PoolAccount 回傳流動性池的 wallet 帳戶名。
func PoolAccount(poolID int64) string { return fmt.Sprintf("%s%d", poolPrefix, poolID) }

// Pools 全部流動性池。
func (s *Store) Pools() ([]Pool, error) {
	rows, err := s.db.Query(`SELECT id, pair, token0, token1, reserve0, reserve1, total_lp, reward_token, reward_per_day, apr, created_at FROM defi_pools ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("defi: 讀取池: %w", err)
	}
	defer rows.Close()
	out := []Pool{}
	for rows.Next() {
		var p Pool
		if err := rows.Scan(&p.ID, &p.Pair, &p.Token0, &p.Token1, &p.Reserve0, &p.Reserve1, &p.TotalLP,
			&p.RewardToken, &p.RewardPerDay, &p.APR, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetPool 單個池。
func (s *Store) GetPool(id int64) (*Pool, error) {
	row := s.db.QueryRow(`SELECT id, pair, token0, token1, reserve0, reserve1, total_lp, reward_token, reward_per_day, apr, created_at FROM defi_pools WHERE id=?`, id)
	var p Pool
	if err := row.Scan(&p.ID, &p.Pair, &p.Token0, &p.Token1, &p.Reserve0, &p.Reserve1, &p.TotalLP,
		&p.RewardToken, &p.RewardPerDay, &p.APR, &p.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("defi: 查詢池: %w", err)
	}
	return &p, nil
}

// LPForUser 用戶在某池的頭寸（無則 nil）。
func (s *Store) LPForUser(address string, poolID int64) (*LPPosition, error) {
	row := s.db.QueryRow(`SELECT id, address, pool_id, lp_amount, token0_amount, token1_amount, last_reward_at, created_at FROM defi_lp_positions WHERE address=? AND pool_id=?`, address, poolID)
	var p LPPosition
	if err := row.Scan(&p.ID, &p.Address, &p.PoolID, &p.LPAmount, &p.Token0Amount, &p.Token1Amount, &p.LastRewardAt, &p.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("defi: 查詢頭寸: %w", err)
	}
	return &p, nil
}

// MyLiquidity 用戶全部頭寸（含池資料與預估待領獎勵）。
func (s *Store) MyLiquidity(address string) ([]LPPosition, error) {
	rows, err := s.db.Query(`SELECT lp.id, lp.address, lp.pool_id, lp.lp_amount, lp.token0_amount, lp.token1_amount, lp.last_reward_at, lp.created_at,
		p.pair, p.token0, p.token1, p.reserve0, p.reserve1, p.total_lp, p.apr, p.reward_per_day
		FROM defi_lp_positions lp JOIN defi_pools p ON lp.pool_id=p.id WHERE lp.address=? ORDER BY lp.id`, address)
	if err != nil {
		return nil, fmt.Errorf("defi: 讀取頭寸: %w", err)
	}
	defer rows.Close()
	now := float64(time.Now().Unix())
	out := []LPPosition{}
	for rows.Next() {
		var p LPPosition
		var rpd float64
		if err := rows.Scan(&p.ID, &p.Address, &p.PoolID, &p.LPAmount, &p.Token0Amount, &p.Token1Amount, &p.LastRewardAt, &p.CreatedAt,
			&p.Pair, &p.Token0, &p.Token1, &p.Reserve0, &p.Reserve1, &p.TotalLP, &p.APR, &rpd); err != nil {
			return nil, err
		}
		if p.TotalLP > 0 {
			ratio := p.LPAmount / p.TotalLP
			diff := (now - float64(p.LastRewardAt)) / 86400
			if diff > 0 {
				p.PendingReward = rpd * ratio * diff
			}
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AddLiquidity 添加流動性（照 Python：LP=sqrt(a0*a1) 或比例式）。
func (s *Store) AddLiquidity(address string, poolID int64, amount0, amount1 float64) (float64, error) {
	if amount0 <= 0 || amount1 <= 0 {
		return 0, fmt.Errorf("兩種代幣數量都必須大於 0")
	}
	pool, err := s.GetPool(poolID)
	if err != nil {
		return 0, err
	}
	if pool == nil {
		return 0, fmt.Errorf("流動性池不存在")
	}
	var lp float64
	if pool.TotalLP <= 0 {
		lp = math.Sqrt(amount0 * amount1)
	} else {
		lp0 := amount0 * pool.TotalLP / pool.Reserve0
		lp1 := amount1 * pool.TotalLP / pool.Reserve1
		lp = math.Min(lp0, lp1)
	}
	if lp <= 0 {
		return 0, fmt.Errorf("LP 數量計算錯誤")
	}
	now := time.Now().Unix()
	if _, err := s.db.Exec(`UPDATE defi_pools SET reserve0=reserve0+?, reserve1=reserve1+?, total_lp=total_lp+? WHERE id=?`,
		amount0, amount1, lp, poolID); err != nil {
		return 0, fmt.Errorf("defi: 更新池: %w", err)
	}
	pos, err := s.LPForUser(address, poolID)
	if err != nil {
		return 0, err
	}
	if pos != nil {
		if _, err := s.db.Exec(`UPDATE defi_lp_positions SET lp_amount=lp_amount+?, token0_amount=token0_amount+?, token1_amount=token1_amount+?, last_reward_at=? WHERE id=?`,
			lp, amount0, amount1, now, pos.ID); err != nil {
			return 0, fmt.Errorf("defi: 更新頭寸: %w", err)
		}
	} else {
		if _, err := s.db.Exec(`INSERT INTO defi_lp_positions(address, pool_id, lp_amount, token0_amount, token1_amount, last_reward_at, created_at)
			VALUES(?,?,?,?,?,?,?)`, address, poolID, lp, amount0, amount1, now, now); err != nil {
			return 0, fmt.Errorf("defi: 新增頭寸: %w", err)
		}
	}
	return lp, nil
}

// RemoveLiquidity 移除流動性（照 Python：比例贖回）。
func (s *Store) RemoveLiquidity(address string, positionID int64, lpAmount float64) (float64, float64, int64, error) {
	if lpAmount <= 0 {
		return 0, 0, 0, fmt.Errorf("LP 數量必須大於 0")
	}
	var pos LPPosition
	if err := s.db.QueryRow(`SELECT id, address, pool_id, lp_amount, token0_amount, token1_amount, last_reward_at, created_at FROM defi_lp_positions WHERE id=? AND address=?`, positionID, address).
		Scan(&pos.ID, &pos.Address, &pos.PoolID, &pos.LPAmount, &pos.Token0Amount, &pos.Token1Amount, &pos.LastRewardAt, &pos.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return 0, 0, 0, fmt.Errorf("頭寸不存在")
		}
		return 0, 0, 0, fmt.Errorf("defi: 查詢頭寸: %w", err)
	}
	if lpAmount > pos.LPAmount {
		return 0, 0, 0, fmt.Errorf("LP 數量不足")
	}
	pool, err := s.GetPool(pos.PoolID)
	if err != nil {
		return 0, 0, 0, err
	}
	if pool == nil || pool.TotalLP <= 0 {
		return 0, 0, 0, fmt.Errorf("池異常")
	}
	ratio := lpAmount / pool.TotalLP
	amount0 := pool.Reserve0 * ratio
	amount1 := pool.Reserve1 * ratio
	if _, err := s.db.Exec(`UPDATE defi_pools SET reserve0=reserve0-?, reserve1=reserve1-?, total_lp=total_lp-? WHERE id=?`,
		amount0, amount1, lpAmount, pos.PoolID); err != nil {
		return 0, 0, 0, fmt.Errorf("defi: 更新池: %w", err)
	}
	newLP := pos.LPAmount - lpAmount
	if newLP <= 0.000001 {
		if _, err := s.db.Exec(`DELETE FROM defi_lp_positions WHERE id=?`, positionID); err != nil {
			return 0, 0, 0, fmt.Errorf("defi: 刪除頭寸: %w", err)
		}
	} else {
		if _, err := s.db.Exec(`UPDATE defi_lp_positions SET lp_amount=?, token0_amount=token0_amount-?, token1_amount=token1_amount-? WHERE id=?`,
			newLP, amount0, amount1, positionID); err != nil {
			return 0, 0, 0, fmt.Errorf("defi: 更新頭寸: %w", err)
		}
	}
	return amount0, amount1, pos.PoolID, nil
}

// LoanByID 單筆貸款。
func (s *Store) LoanByID(id int64) (*Loan, error) {
	row := s.db.QueryRow(`SELECT id, address, collateral_asset, collateral_amount, borrow_asset, borrow_amount,
		collateral_value, loan_value, ltv, liquidation_price, interest_accrued, status, last_update, created_at
		FROM defi_loans WHERE id=?`, id)
	var l Loan
	if err := row.Scan(&l.ID, &l.Address, &l.CollateralAsset, &l.CollateralAmount, &l.BorrowAsset, &l.BorrowAmount,
		&l.CollateralValue, &l.LoanValue, &l.LTV, &l.LiquidationPrice, &l.InterestAccrued, &l.Status, &l.LastUpdate, &l.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("defi: 查詢貸款: %w", err)
	}
	return &l, nil
}

// ClaimLPReward 領取流動性獎勵（照 Python：reward_per_day × 占比 × 天數）。
func (s *Store) ClaimLPReward(address string, positionID int64) (float64, string, error) {
	var pos LPPosition
	if err := s.db.QueryRow(`SELECT id, address, pool_id, lp_amount, token0_amount, token1_amount, last_reward_at, created_at FROM defi_lp_positions WHERE id=? AND address=?`, positionID, address).
		Scan(&pos.ID, &pos.Address, &pos.PoolID, &pos.LPAmount, &pos.Token0Amount, &pos.Token1Amount, &pos.LastRewardAt, &pos.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return 0, "", fmt.Errorf("頭寸不存在")
		}
		return 0, "", fmt.Errorf("defi: 查詢頭寸: %w", err)
	}
	pool, err := s.GetPool(pos.PoolID)
	if err != nil {
		return 0, "", err
	}
	if pool == nil {
		return 0, "", fmt.Errorf("池不存在")
	}
	now := time.Now().Unix()
	diff := now - pos.LastRewardAt
	if diff <= 0 {
		return 0, "", fmt.Errorf("暫無獎勵")
	}
	ratio := 0.0
	if pool.TotalLP > 0 {
		ratio = pos.LPAmount / pool.TotalLP
	}
	reward := pool.RewardPerDay * ratio * (float64(diff) / 86400)
	if reward <= 0 {
		return 0, "", fmt.Errorf("獎勵為 0")
	}
	if _, err := s.db.Exec(`UPDATE defi_lp_positions SET last_reward_at=? WHERE id=?`, now, positionID); err != nil {
		return 0, "", fmt.Errorf("defi: 更新獎勵時間: %w", err)
	}
	return reward, pool.RewardToken, nil
}

// LendingMarkets 全部啟用借貸市場。
func (s *Store) LendingMarkets() ([]LendingMarket, error) {
	rows, err := s.db.Query(`SELECT id, asset, total_deposit, total_borrow, deposit_apr, borrow_apr, collateral_factor, liquidation_threshold, enabled FROM defi_lending_markets WHERE enabled=1 ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("defi: 讀取市場: %w", err)
	}
	defer rows.Close()
	out := []LendingMarket{}
	for rows.Next() {
		var m LendingMarket
		if err := rows.Scan(&m.ID, &m.Asset, &m.TotalDeposit, &m.TotalBorrow, &m.DepositAPR, &m.BorrowAPR,
			&m.CollateralFactor, &m.LiquidationThreshold, &m.Enabled); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetMarket 單個借貸市場。
func (s *Store) GetMarket(asset string) (*LendingMarket, error) {
	row := s.db.QueryRow(`SELECT id, asset, total_deposit, total_borrow, deposit_apr, borrow_apr, collateral_factor, liquidation_threshold, enabled FROM defi_lending_markets WHERE asset=?`, asset)
	var m LendingMarket
	if err := row.Scan(&m.ID, &m.Asset, &m.TotalDeposit, &m.TotalBorrow, &m.DepositAPR, &m.BorrowAPR,
		&m.CollateralFactor, &m.LiquidationThreshold, &m.Enabled); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("defi: 查詢市場: %w", err)
	}
	return &m, nil
}

// DepositLending 存款到借貸市場（照 Python 合併存款）。
func (s *Store) DepositLending(address, asset string, amount float64) error {
	if amount <= 0 {
		return fmt.Errorf("存款必須大於 0")
	}
	market, err := s.GetMarket(asset)
	if err != nil {
		return err
	}
	if market == nil {
		return fmt.Errorf("借貸市場不存在")
	}
	now := time.Now().Unix()
	var depID int64
	err = s.db.QueryRow(`SELECT id FROM defi_deposits WHERE address=? AND asset=?`, address, asset).Scan(&depID)
	if err == sql.ErrNoRows {
		if _, err := s.db.Exec(`INSERT INTO defi_deposits(address, asset, amount, last_update, created_at) VALUES(?,?,?,?,?)`,
			address, asset, amount, now, now); err != nil {
			return fmt.Errorf("defi: 新增存款: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("defi: 查詢存款: %w", err)
	} else {
		if _, err := s.db.Exec(`UPDATE defi_deposits SET amount=amount+?, last_update=? WHERE id=?`, amount, now, depID); err != nil {
			return fmt.Errorf("defi: 更新存款: %w", err)
		}
	}
	if _, err := s.db.Exec(`UPDATE defi_lending_markets SET total_deposit=total_deposit+? WHERE asset=?`, amount, asset); err != nil {
		return fmt.Errorf("defi: 更新市場: %w", err)
	}
	return nil
}

// WithdrawLending 借貸市場取款（照 Python 餘額檢查）。
func (s *Store) WithdrawLending(address, asset string, amount float64) error {
	if amount <= 0 {
		return fmt.Errorf("取款必須大於 0")
	}
	var depID int64
	var depAmount float64
	err := s.db.QueryRow(`SELECT id, amount FROM defi_deposits WHERE address=? AND asset=?`, address, asset).Scan(&depID, &depAmount)
	if err == sql.ErrNoRows {
		return fmt.Errorf("存款餘額不足")
	}
	if err != nil {
		return fmt.Errorf("defi: 查詢存款: %w", err)
	}
	if depAmount < amount {
		return fmt.Errorf("存款餘額不足")
	}
	if _, err := s.db.Exec(`UPDATE defi_deposits SET amount=amount-? WHERE id=?`, amount, depID); err != nil {
		return fmt.Errorf("defi: 更新存款: %w", err)
	}
	if _, err := s.db.Exec(`UPDATE defi_lending_markets SET total_deposit=total_deposit-? WHERE asset=?`, amount, asset); err != nil {
		return fmt.Errorf("defi: 更新市場: %w", err)
	}
	return nil
}

// Borrow 抵押借款（照 Python：抵押值簡化=數量；max=抵押×collateral_factor）。
func (s *Store) Borrow(address, collateralAsset string, collateralAmount float64, borrowAsset string, borrowAmount float64) (int64, error) {
	if collateralAmount <= 0 || borrowAmount <= 0 {
		return 0, fmt.Errorf("抵押與借款數量必須大於 0")
	}
	cMarket, err := s.GetMarket(collateralAsset)
	if err != nil {
		return 0, err
	}
	bMarket, err := s.GetMarket(borrowAsset)
	if err != nil {
		return 0, err
	}
	if cMarket == nil || bMarket == nil {
		return 0, fmt.Errorf("借貸市場不存在")
	}
	collateralValue := collateralAmount // 簡化：抵押資產價值=數量（照 Python）
	maxBorrow := collateralValue * cMarket.CollateralFactor
	if borrowAmount > maxBorrow {
		return 0, fmt.Errorf("超出最大可借金額 %.2f", maxBorrow)
	}
	now := time.Now().Unix()
	liquidationPrice := 0.0
	if borrowAmount > 0 {
		liquidationPrice = collateralAmount * cMarket.LiquidationThreshold / borrowAmount
	}
	res, err := s.db.Exec(`INSERT INTO defi_loans(address, collateral_asset, collateral_amount, borrow_asset, borrow_amount,
		collateral_value, loan_value, ltv, liquidation_price, status, last_update, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,'active',?,?)`,
		address, collateralAsset, collateralAmount, borrowAsset, borrowAmount,
		collateralValue, borrowAmount, borrowAmount/collateralValue, liquidationPrice, now, now)
	if err != nil {
		return 0, fmt.Errorf("defi: 新增貸款: %w", err)
	}
	if _, err := s.db.Exec(`UPDATE defi_lending_markets SET total_borrow=total_borrow+? WHERE asset=?`, borrowAmount, borrowAsset); err != nil {
		return 0, fmt.Errorf("defi: 更新市場: %w", err)
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// interestFor 計算應計利息（10% 年利率，照 Python）。
func interestFor(principal float64, from, now int64) float64 {
	if now <= from {
		return 0
	}
	return principal * annualInterest * (float64(now-from) / 31536000)
}

// RepayLoan 還款（照 Python：全額含息→repaid＋歸還抵押；部分→減少本金）。
func (s *Store) RepayLoan(address string, loanID int64, amount float64) (map[string]float64, error) {
	if amount <= 0 {
		return nil, fmt.Errorf("還款必須大於 0")
	}
	var l Loan
	if err := s.db.QueryRow(`SELECT id, address, collateral_asset, collateral_amount, borrow_asset, borrow_amount,
		collateral_value, loan_value, ltv, liquidation_price, interest_accrued, status, last_update, created_at
		FROM defi_loans WHERE id=? AND address=? AND status='active'`, loanID, address).
		Scan(&l.ID, &l.Address, &l.CollateralAsset, &l.CollateralAmount, &l.BorrowAsset, &l.BorrowAmount,
			&l.CollateralValue, &l.LoanValue, &l.LTV, &l.LiquidationPrice, &l.InterestAccrued, &l.Status, &l.LastUpdate, &l.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("貸款不存在")
		}
		return nil, fmt.Errorf("defi: 查詢貸款: %w", err)
	}
	now := time.Now().Unix()
	interest := interestFor(l.BorrowAmount, l.LastUpdate, now)
	totalOwed := l.BorrowAmount + interest
	out := map[string]float64{}
	if amount >= totalOwed {
		if _, err := s.db.Exec(`UPDATE defi_loans SET status='repaid', borrow_amount=0, interest_accrued=interest_accrued+?, last_update=? WHERE id=?`,
			interest, now, loanID); err != nil {
			return nil, fmt.Errorf("defi: 還款完成: %w", err)
		}
		if _, err := s.db.Exec(`UPDATE defi_lending_markets SET total_borrow=total_borrow-? WHERE asset=?`, l.BorrowAmount, l.BorrowAsset); err != nil {
			return nil, fmt.Errorf("defi: 更新市場: %w", err)
		}
		out["repaid"] = totalOwed
		out["collateral_returned"] = l.CollateralAmount
		out["status"] = 1 // repaid
		return out, nil
	}
	// 部分還款
	if _, err := s.db.Exec(`UPDATE defi_loans SET borrow_amount=borrow_amount-?, interest_accrued=interest_accrued+?, last_update=? WHERE id=?`,
		amount, interest, now, loanID); err != nil {
		return nil, fmt.Errorf("defi: 部分還款: %w", err)
	}
	if _, err := s.db.Exec(`UPDATE defi_lending_markets SET total_borrow=total_borrow-? WHERE asset=?`, amount, l.BorrowAsset); err != nil {
		return nil, fmt.Errorf("defi: 更新市場: %w", err)
	}
	out["repaid"] = amount
	out["remaining"] = l.BorrowAmount - amount + interest
	out["status"] = 0 // active
	return out, nil
}

// MyDeposits 用戶存款。
func (s *Store) MyDeposits(address string) ([]LendingDeposit, error) {
	rows, err := s.db.Query(`SELECT id, address, asset, amount, last_update, created_at FROM defi_deposits WHERE address=? ORDER BY id`, address)
	if err != nil {
		return nil, fmt.Errorf("defi: 讀取存款: %w", err)
	}
	defer rows.Close()
	out := []LendingDeposit{}
	for rows.Next() {
		var d LendingDeposit
		if err := rows.Scan(&d.ID, &d.Address, &d.Asset, &d.Amount, &d.LastUpdate, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// MyLoans 用戶貸款（含截至查詢的應計利息）。
func (s *Store) MyLoans(address string) ([]Loan, error) {
	rows, err := s.db.Query(`SELECT id, address, collateral_asset, collateral_amount, borrow_asset, borrow_amount,
		collateral_value, loan_value, ltv, liquidation_price, interest_accrued, status, last_update, created_at
		FROM defi_loans WHERE address=? ORDER BY id`, address)
	if err != nil {
		return nil, fmt.Errorf("defi: 讀取貸款: %w", err)
	}
	defer rows.Close()
	now := time.Now().Unix()
	out := []Loan{}
	for rows.Next() {
		var l Loan
		if err := rows.Scan(&l.ID, &l.Address, &l.CollateralAsset, &l.CollateralAmount, &l.BorrowAsset, &l.BorrowAmount,
			&l.CollateralValue, &l.LoanValue, &l.LTV, &l.LiquidationPrice, &l.InterestAccrued, &l.Status, &l.LastUpdate, &l.CreatedAt); err != nil {
			return nil, err
		}
		if l.Status == "active" {
			l.InterestNow = interestFor(l.BorrowAmount, l.LastUpdate, now)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ==================== IDO 平台 ====================

// IDOProject IDO 項目視圖。
type IDOProject struct {
	ID                int64   `json:"id"`
	Name              string  `json:"name"`
	Symbol            string  `json:"symbol"`
	TokenPrice        float64 `json:"token_price"`
	RaiseTarget       float64 `json:"raise_target"`
	RaiseAsset        string  `json:"raise_asset"`
	MinBuy            float64 `json:"min_buy"`
	MaxBuy            float64 `json:"max_buy"`
	StartTime         int64   `json:"start_time"`
	EndTime           int64   `json:"end_time"`
	Status            string  `json:"status"`
	Description       string  `json:"description"`
	TotalRaised       float64 `json:"total_raised"`
	TotalParticipants int64   `json:"total_participants"`
	CreatedAt         int64   `json:"created_at"`
}

// IDOSubscription 用戶認購視圖。
type IDOSubscription struct {
	ID              int64   `json:"id"`
	Address         string  `json:"address"`
	ProjectID       int64   `json:"project_id"`
	SubscribeAmount float64 `json:"subscribe_amount"`
	AllocatedTokens float64 `json:"allocated_tokens"`
	Claimed         bool    `json:"claimed"`
	CreatedAt       int64   `json:"created_at"`
	Name            string  `json:"name"`
	Symbol          string  `json:"symbol"`
	TokenPrice      float64 `json:"token_price"`
	Status          string  `json:"status"`
	EndTime         int64   `json:"end_time"`
}

// IDOProjects 全部 IDO 項目（開始時間升序）。
func (s *Store) IDOProjects() ([]IDOProject, error) {
	rows, err := s.db.Query(`SELECT id, name, symbol, token_price, raise_target, raise_asset, min_buy, max_buy,
		start_time, end_time, status, description, total_raised, total_participants, created_at FROM defi_ido_projects ORDER BY start_time`)
	if err != nil {
		return nil, fmt.Errorf("defi: 讀取 IDO: %w", err)
	}
	defer rows.Close()
	out := []IDOProject{}
	for rows.Next() {
		var p IDOProject
		if err := rows.Scan(&p.ID, &p.Name, &p.Symbol, &p.TokenPrice, &p.RaiseTarget, &p.RaiseAsset, &p.MinBuy, &p.MaxBuy,
			&p.StartTime, &p.EndTime, &p.Status, &p.Description, &p.TotalRaised, &p.TotalParticipants, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetIDOProject 單個 IDO 項目。
func (s *Store) GetIDOProject(id int64) (*IDOProject, error) {
	row := s.db.QueryRow(`SELECT id, name, symbol, token_price, raise_target, raise_asset, min_buy, max_buy,
		start_time, end_time, status, description, total_raised, total_participants, created_at FROM defi_ido_projects WHERE id=?`, id)
	var p IDOProject
	if err := row.Scan(&p.ID, &p.Name, &p.Symbol, &p.TokenPrice, &p.RaiseTarget, &p.RaiseAsset, &p.MinBuy, &p.MaxBuy,
		&p.StartTime, &p.EndTime, &p.Status, &p.Description, &p.TotalRaised, &p.TotalParticipants, &p.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("defi: 查詢 IDO: %w", err)
	}
	return &p, nil
}

// MyIDO 用戶認購（含項目資料）。
func (s *Store) MyIDO(address string) ([]IDOSubscription, error) {
	rows, err := s.db.Query(`SELECT sub.id, sub.address, sub.project_id, sub.subscribe_amount, sub.allocated_tokens, sub.claimed, sub.created_at,
		p.name, p.symbol, p.token_price, p.status, p.end_time
		FROM defi_ido_subscriptions sub JOIN defi_ido_projects p ON sub.project_id=p.id WHERE sub.address=? ORDER BY sub.id`, address)
	if err != nil {
		return nil, fmt.Errorf("defi: 讀取認購: %w", err)
	}
	defer rows.Close()
	out := []IDOSubscription{}
	for rows.Next() {
		var x IDOSubscription
		if err := rows.Scan(&x.ID, &x.Address, &x.ProjectID, &x.SubscribeAmount, &x.AllocatedTokens, &x.Claimed, &x.CreatedAt,
			&x.Name, &x.Symbol, &x.TokenPrice, &x.Status, &x.EndTime); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// SubscribeIDO 認購 IDO（時間窗/上下限/一人一項目，照 Python）。
func (s *Store) SubscribeIDO(address string, projectID, amount float64) (float64, error) {
	if amount <= 0 {
		return 0, fmt.Errorf("認購金額必須大於 0")
	}
	p, err := s.GetIDOProject(int64(projectID))
	if err != nil {
		return 0, err
	}
	if p == nil {
		return 0, fmt.Errorf("項目不存在")
	}
	now := time.Now().Unix()
	if now < p.StartTime {
		return 0, fmt.Errorf("認購尚未開始")
	}
	if now > p.EndTime {
		return 0, fmt.Errorf("認購已結束")
	}
	if amount < p.MinBuy {
		return 0, fmt.Errorf("最低認購 %.2f %s", p.MinBuy, p.RaiseAsset)
	}
	if amount > p.MaxBuy {
		return 0, fmt.Errorf("最高認購 %.2f %s", p.MaxBuy, p.RaiseAsset)
	}
	var cnt int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM defi_ido_subscriptions WHERE address=? AND project_id=?`, address, int64(projectID)).Scan(&cnt); err != nil {
		return 0, fmt.Errorf("defi: 檢查認購: %w", err)
	}
	if cnt > 0 {
		return 0, fmt.Errorf("已認購此項目")
	}
	tokens := amount / p.TokenPrice
	if _, err := s.db.Exec(`INSERT INTO defi_ido_subscriptions(address, project_id, subscribe_amount, allocated_tokens, claimed, created_at) VALUES(?,?,?,?,0,?)`,
		address, int64(projectID), amount, tokens, now); err != nil {
		return 0, fmt.Errorf("defi: 新增認購: %w", err)
	}
	if _, err := s.db.Exec(`UPDATE defi_ido_projects SET total_raised=total_raised+?, total_participants=total_participants+1 WHERE id=?`,
		amount, int64(projectID)); err != nil {
		return 0, fmt.Errorf("defi: 更新 IDO: %w", err)
	}
	return tokens, nil
}

// ClaimIDOTokens 領取 IDO 代幣（項目 completed 才可領，照 Python）。
func (s *Store) ClaimIDOTokens(address string, subscriptionID int64) (float64, string, error) {
	var x IDOSubscription
	if err := s.db.QueryRow(`SELECT id, address, project_id, subscribe_amount, allocated_tokens, claimed, created_at FROM defi_ido_subscriptions WHERE id=? AND address=?`, subscriptionID, address).
		Scan(&x.ID, &x.Address, &x.ProjectID, &x.SubscribeAmount, &x.AllocatedTokens, &x.Claimed, &x.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return 0, "", fmt.Errorf("認購記錄不存在")
		}
		return 0, "", fmt.Errorf("defi: 查詢認購: %w", err)
	}
	if x.Claimed {
		return 0, "", fmt.Errorf("已領取")
	}
	p, err := s.GetIDOProject(x.ProjectID)
	if err != nil {
		return 0, "", err
	}
	if p == nil || p.Status != "completed" {
		return 0, "", fmt.Errorf("項目尚未結束，暫不能領取")
	}
	if _, err := s.db.Exec(`UPDATE defi_ido_subscriptions SET claimed=1 WHERE id=?`, subscriptionID); err != nil {
		return 0, "", fmt.Errorf("defi: 認購領取標記: %w", err)
	}
	return x.AllocatedTokens, p.Symbol, nil
}

// ==================== 收益聚合器（Vault） ====================

// Vault 金庫視圖。
type Vault struct {
	ID              int64   `json:"id"`
	Name            string  `json:"name"`
	Strategy        string  `json:"strategy"`
	UnderlyingAsset string  `json:"underlying_asset"`
	TotalAssets     float64 `json:"total_assets"`
	TotalShares     float64 `json:"total_shares"`
	APR             float64 `json:"apr"`
	MinDeposit      float64 `json:"min_deposit"`
	PerformanceFee  float64 `json:"performance_fee"`
	Status          string  `json:"status"`
	Description     string  `json:"description"`
	CreatedAt       int64   `json:"created_at"`
}

// VaultPosition 用戶金庫頭寸視圖。
type VaultPosition struct {
	ID              int64   `json:"id"`
	Address         string  `json:"address"`
	VaultID         int64   `json:"vault_id"`
	Shares          float64 `json:"shares"`
	UnderlyingAmt   float64 `json:"underlying_amount"`
	LastCompound    int64   `json:"last_compound"`
	CreatedAt       int64   `json:"created_at"`
	Name            string  `json:"name"`
	Strategy        string  `json:"strategy"`
	UnderlyingAsset string  `json:"underlying_asset"`
	APR             float64 `json:"apr"`
	TotalAssets     float64 `json:"total_assets"`
	TotalShares     float64 `json:"total_shares"`
	ValueNow        float64 `json:"value_now"` // 按目前份額估算的資產值
}

// VaultPosByID 單筆金庫頭寸。
func (s *Store) VaultPosByID(id int64) (*VaultPosition, error) {
	row := s.db.QueryRow(`SELECT id, address, vault_id, shares, underlying_amount, last_compound, created_at FROM defi_vault_positions WHERE id=?`, id)
	var p VaultPosition
	if err := row.Scan(&p.ID, &p.Address, &p.VaultID, &p.Shares, &p.UnderlyingAmt, &p.LastCompound, &p.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("defi: 查詢金庫頭寸: %w", err)
	}
	return &p, nil
}

// SubByID 單筆認購。
func (s *Store) SubByID(id int64) (*IDOSubscription, error) {
	row := s.db.QueryRow(`SELECT id, address, project_id, subscribe_amount, allocated_tokens, claimed, created_at FROM defi_ido_subscriptions WHERE id=?`, id)
	var x IDOSubscription
	if err := row.Scan(&x.ID, &x.Address, &x.ProjectID, &x.SubscribeAmount, &x.AllocatedTokens, &x.Claimed, &x.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("defi: 查詢認購: %w", err)
	}
	return &x, nil
}

// VaultAccount 回傳金庫的 wallet 帳戶名。
func VaultAccount(vaultID int64) string { return fmt.Sprintf("defi_vault_%d", vaultID) }

// Vaults 全部啟用金庫。
func (s *Store) Vaults() ([]Vault, error) {
	rows, err := s.db.Query(`SELECT id, name, strategy, underlying_asset, total_assets, total_shares, apr, min_deposit, performance_fee, status, description, created_at FROM defi_vaults WHERE status='active' ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("defi: 讀取金庫: %w", err)
	}
	defer rows.Close()
	out := []Vault{}
	for rows.Next() {
		var v Vault
		if err := rows.Scan(&v.ID, &v.Name, &v.Strategy, &v.UnderlyingAsset, &v.TotalAssets, &v.TotalShares, &v.APR,
			&v.MinDeposit, &v.PerformanceFee, &v.Status, &v.Description, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetVault 單個金庫。
func (s *Store) GetVault(id int64) (*Vault, error) {
	row := s.db.QueryRow(`SELECT id, name, strategy, underlying_asset, total_assets, total_shares, apr, min_deposit, performance_fee, status, description, created_at FROM defi_vaults WHERE id=?`, id)
	var v Vault
	if err := row.Scan(&v.ID, &v.Name, &v.Strategy, &v.UnderlyingAsset, &v.TotalAssets, &v.TotalShares, &v.APR,
		&v.MinDeposit, &v.PerformanceFee, &v.Status, &v.Description, &v.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("defi: 查詢金庫: %w", err)
	}
	return &v, nil
}

// MyVaults 用戶金庫頭寸。
func (s *Store) MyVaults(address string) ([]VaultPosition, error) {
	rows, err := s.db.Query(`SELECT vp.id, vp.address, vp.vault_id, vp.shares, vp.underlying_amount, vp.last_compound, vp.created_at,
		v.name, v.strategy, v.underlying_asset, v.apr, v.total_assets, v.total_shares
		FROM defi_vault_positions vp JOIN defi_vaults v ON vp.vault_id=v.id WHERE vp.address=? ORDER BY vp.id`, address)
	if err != nil {
		return nil, fmt.Errorf("defi: 讀取金庫頭寸: %w", err)
	}
	defer rows.Close()
	out := []VaultPosition{}
	for rows.Next() {
		var p VaultPosition
		if err := rows.Scan(&p.ID, &p.Address, &p.VaultID, &p.Shares, &p.UnderlyingAmt, &p.LastCompound, &p.CreatedAt,
			&p.Name, &p.Strategy, &p.UnderlyingAsset, &p.APR, &p.TotalAssets, &p.TotalShares); err != nil {
			return nil, err
		}
		if p.TotalShares > 0 {
			p.ValueNow = p.Shares * p.TotalAssets / p.TotalShares
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DepositVault 存入金庫（shares=amount 首存／比例式，照 Python）。
func (s *Store) DepositVault(address string, vaultID float64, amount float64) (float64, error) {
	if amount <= 0 {
		return 0, fmt.Errorf("存入金額必須大於 0")
	}
	v, err := s.GetVault(int64(vaultID))
	if err != nil {
		return 0, err
	}
	if v == nil {
		return 0, fmt.Errorf("金庫不存在")
	}
	if amount < v.MinDeposit {
		return 0, fmt.Errorf("最低存入 %.2f %s", v.MinDeposit, v.UnderlyingAsset)
	}
	var shares float64
	if v.TotalShares <= 0 {
		shares = amount
	} else {
		shares = amount * v.TotalShares / v.TotalAssets
	}
	now := time.Now().Unix()
	pos, err := s.vaultPos(address, int64(vaultID))
	if err != nil {
		return 0, err
	}
	if pos != nil {
		if _, err := s.db.Exec(`UPDATE defi_vault_positions SET shares=shares+?, underlying_amount=underlying_amount+?, last_compound=? WHERE id=?`,
			shares, amount, now, pos.ID); err != nil {
			return 0, fmt.Errorf("defi: 更新金庫頭寸: %w", err)
		}
	} else {
		if _, err := s.db.Exec(`INSERT INTO defi_vault_positions(address, vault_id, shares, underlying_amount, last_compound, created_at) VALUES(?,?,?,?,?,?)`,
			address, int64(vaultID), shares, amount, now, now); err != nil {
			return 0, fmt.Errorf("defi: 新增金庫頭寸: %w", err)
		}
	}
	if _, err := s.db.Exec(`UPDATE defi_vaults SET total_assets=total_assets+?, total_shares=total_shares+? WHERE id=?`,
		amount, shares, int64(vaultID)); err != nil {
		return 0, fmt.Errorf("defi: 更新金庫: %w", err)
	}
	return shares, nil
}

// vaultPos 用戶在某金庫的頭寸（無則 nil）。
func (s *Store) vaultPos(address string, vaultID int64) (*VaultPosition, error) {
	row := s.db.QueryRow(`SELECT id, address, vault_id, shares, underlying_amount, last_compound, created_at FROM defi_vault_positions WHERE address=? AND vault_id=?`, address, vaultID)
	var p VaultPosition
	if err := row.Scan(&p.ID, &p.Address, &p.VaultID, &p.Shares, &p.UnderlyingAmt, &p.LastCompound, &p.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("defi: 查詢金庫頭寸: %w", err)
	}
	return &p, nil
}

// WithdrawVault 取出金庫（份額兌換比例贖回，照 Python）。
func (s *Store) WithdrawVault(address string, positionID, shares float64) (float64, error) {
	if shares <= 0 {
		return 0, fmt.Errorf("份額必須大於 0")
	}
	var pos VaultPosition
	if err := s.db.QueryRow(`SELECT id, address, vault_id, shares, underlying_amount, last_compound, created_at FROM defi_vault_positions WHERE id=? AND address=?`, positionID, address).
		Scan(&pos.ID, &pos.Address, &pos.VaultID, &pos.Shares, &pos.UnderlyingAmt, &pos.LastCompound, &pos.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return 0, fmt.Errorf("頭寸不存在")
		}
		return 0, fmt.Errorf("defi: 查詢金庫頭寸: %w", err)
	}
	if shares > pos.Shares {
		return 0, fmt.Errorf("份額不足")
	}
	v, err := s.GetVault(pos.VaultID)
	if err != nil {
		return 0, err
	}
	if v == nil || v.TotalShares <= 0 {
		return 0, fmt.Errorf("金庫異常")
	}
	amount := shares * v.TotalAssets / v.TotalShares
	if _, err := s.db.Exec(`UPDATE defi_vaults SET total_assets=total_assets-?, total_shares=total_shares-? WHERE id=?`,
		amount, shares, pos.VaultID); err != nil {
		return 0, fmt.Errorf("defi: 更新金庫: %w", err)
	}
	newShares := pos.Shares - shares
	if newShares <= 0.000001 {
		if _, err := s.db.Exec(`DELETE FROM defi_vault_positions WHERE id=?`, positionID); err != nil {
			return 0, fmt.Errorf("defi: 刪除金庫頭寸: %w", err)
		}
	} else {
		if _, err := s.db.Exec(`UPDATE defi_vault_positions SET shares=?, underlying_amount=underlying_amount-? WHERE id=?`,
			newShares, amount, positionID); err != nil {
			return 0, fmt.Errorf("defi: 更新金庫頭寸: %w", err)
		}
	}
	return amount, nil
}

// CompoundVault 復投收益（≥CompoundMinSec 間隔，yield=total×apr×占比×年化，照 Python）。
func (s *Store) CompoundVault(address string, positionID int64) (float64, float64, error) {
	var pos VaultPosition
	if err := s.db.QueryRow(`SELECT id, address, vault_id, shares, underlying_amount, last_compound, created_at FROM defi_vault_positions WHERE id=? AND address=?`, positionID, address).
		Scan(&pos.ID, &pos.Address, &pos.VaultID, &pos.Shares, &pos.UnderlyingAmt, &pos.LastCompound, &pos.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return 0, 0, fmt.Errorf("頭寸不存在")
		}
		return 0, 0, fmt.Errorf("defi: 查詢金庫頭寸: %w", err)
	}
	v, err := s.GetVault(pos.VaultID)
	if err != nil {
		return 0, 0, err
	}
	if v == nil {
		return 0, 0, fmt.Errorf("金庫不存在")
	}
	now := time.Now().Unix()
	diff := now - pos.LastCompound
	if diff < s.CompoundMinSec {
		return 0, 0, fmt.Errorf("復投間隔太短（需 ≥%d 秒），請稍後再試", s.CompoundMinSec)
	}
	ratio := 0.0
	if v.TotalShares > 0 {
		ratio = pos.Shares / v.TotalShares
	}
	yield := v.TotalAssets * v.APR * ratio * (float64(diff) / 31536000)
	if yield <= 0 {
		return 0, 0, fmt.Errorf("暫無收益")
	}
	var newShares float64
	if v.TotalAssets > 0 {
		newShares = yield * v.TotalShares / v.TotalAssets
	}
	if _, err := s.db.Exec(`UPDATE defi_vault_positions SET shares=shares+?, underlying_amount=underlying_amount+?, last_compound=? WHERE id=?`,
		newShares, yield, now, positionID); err != nil {
		return 0, 0, fmt.Errorf("defi: 復投頭寸: %w", err)
	}
	if _, err := s.db.Exec(`UPDATE defi_vaults SET total_assets=total_assets+?, total_shares=total_shares+? WHERE id=?`,
		yield, newShares, pos.VaultID); err != nil {
		return 0, 0, fmt.Errorf("defi: 復投金庫: %w", err)
	}
	return yield, newShares, nil
}
