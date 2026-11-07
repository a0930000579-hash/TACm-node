// Package dex 實現 TAC 鏈上的去中心化交易所（AMM 恆定乘積做市）引擎。
//
// 設計要點（符合區塊鏈需求與商業價值）：
//   - 恆定乘積公式 x*y=k：Swap 價格隨儲備自動滑動，與 Uniswap V2 同源。
//   - 所有代幣移動皆透過 VM 標準代幣合約的 transfer（由節點注入的回調執行），
//     交易以 memo 上鏈、鏈上可審計——池帳戶由 crypto.DeriveKey 確定性派生
//     （節點無需保管池私鑰檔案，同一種子+標籤恆得同一地址）。
//   - LP 份額按「注入比例」分配（首次注入 sqrt(amount0*amount1)），
//     RemoveLiquidity 按份額比例退回兩幣，支援完整的提供/退出流動性閉環。
//   - 純 Go 慣用寫法：Engine 以互斥鎖串行化所有狀態變更（AMM 儲備不可並發改動），
//     所有公開方法完整 error handling。
package dex

import (
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// 常量：恆定乘積參數。
const (
	// FeeBps 為每筆 Swap 的手續費基準（0.3%，與主流 AMM 一致）。
	FeeBps int64 = 30
	// MaxShares 防止 LP 份額爆量。
	MaxShares = "1000000000000000000"
)

// 錯誤定義（完整 error handling）。
var (
	ErrNotFound      = errors.New("dex: 池不存在")
	ErrBadAmount     = errors.New("dex: 數量必須大於 0")
	ErrSameToken     = errors.New("dex: 兩個代幣不可相同")
	ErrUnknownToken  = errors.New("dex: 代幣不在池中")
	ErrBadShare      = errors.New("dex: 份額無效")
	ErrInsufficient  = errors.New("dex: 流動性不足")
	ErrSlippage      = errors.New("dex: 滑點超出容忍（min_out 未達成）")
	ErrNoPosition    = errors.New("dex: 無此持倉")
	ErrDuplicatePool = errors.New("dex: 該交易對已存在")
)

// Pool 為一個 AMM 流動性池（x*y=k）。
type Pool struct {
	ID        string   // 池編號（p1、p2…）
	Token0    string   // tx0 標準代幣地址（代幣 0）
	Token1    string   // tx0 標準代幣地址（代幣 1）
	PoolAddr  string   // 池帳戶地址（DeriveKey 派生；持有兩幣儲備）
	Reserve0  *big.Int // 代幣 0 儲備
	Reserve1  *big.Int // 代幣 1 儲備
	LPTotal   *big.Int // 總 LP 份額
	CreatedAt int64
}

// Position 為一位 LP 的持倉。
type Position struct {
	PoolID string
	Owner  string
	Shares *big.Int
}

// TransferFn 執行一筆標準代幣 transfer 並上鏈（由 node 注入）。
// poolID 讓節點能為「池帳戶」確定性派生簽名密鑰；from 為 admin 或池地址時
// 各自使用對應簽名者。返回錯誤則整筆操作失敗。
type TransferFn func(poolID, from, to, contract string, amount *big.Int) error

// BalanceFn 讀取某持有人對某代幣合約的當前餘額（由 node 注入，讀 VM 世界狀態）。
type BalanceFn func(holder, contract string) (*big.Int, error)

// Engine 是 AMM 引擎：所有狀態變更在單一互斥鎖下串行執行。
type Engine struct {
	mu       sync.Mutex
	db       *sql.DB
	admin    string // 做市帳戶（節點 admin；農場注資來源）
	transfer TransferFn
	balance  BalanceFn
}

// New 建立引擎（dataDir 下 dex.db 持久化；回調必填）。
func New(dataDir, admin string, transfer TransferFn, balance BalanceFn) (*Engine, error) {
	if transfer == nil || balance == nil {
		return nil, errors.New("dex: 回調未注入")
	}
	if admin == "" {
		return nil, errors.New("dex: 做市帳戶為空")
	}
	db, err := sql.Open("sqlite", dataDir+"/dex.db")
	if err != nil {
		return nil, fmt.Errorf("dex: 開啟資料庫失敗: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("dex: 資料庫連線失敗: %w", err)
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS pools (
			id TEXT PRIMARY KEY,
			token0 TEXT NOT NULL, token1 TEXT NOT NULL,
			pool_addr TEXT NOT NULL,
			reserve0 TEXT NOT NULL, reserve1 TEXT NOT NULL,
			lp_total TEXT NOT NULL, created_at INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS positions (
			pool_id TEXT NOT NULL, owner TEXT NOT NULL,
			shares TEXT NOT NULL,
			PRIMARY KEY (pool_id, owner))`,
		`CREATE TABLE IF NOT EXISTS stake_pools (
			pool_id TEXT PRIMARY KEY,
			reward_token TEXT NOT NULL,
			reward_per_block TEXT NOT NULL,
			total_staked TEXT NOT NULL,
			acc_per_share TEXT NOT NULL,
			last_tick INTEGER NOT NULL,
			funded TEXT NOT NULL,
			created_at INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS stake_positions (
			pool_id TEXT NOT NULL, owner TEXT NOT NULL,
			shares TEXT NOT NULL, debt TEXT NOT NULL,
			PRIMARY KEY (pool_id, owner))`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("dex: 初始化失敗: %w", err)
		}
	}
	return &Engine{db: db, admin: admin, transfer: transfer, balance: balance}, nil
}

// Close 關閉資料庫。
func (e *Engine) Close() error {
	return e.db.Close()
}

// Pools 回傳全部池（按建立順序）。
func (e *Engine) Pools() ([]*Pool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rows, err := e.db.Query(
		`SELECT id, token0, token1, pool_addr, reserve0, reserve1, lp_total, created_at
		 FROM pools ORDER BY rowid`)
	if err != nil {
		return nil, fmt.Errorf("dex: 讀取池列表失敗: %w", err)
	}
	defer rows.Close()
	var out []*Pool
	for rows.Next() {
		p := &Pool{}
		var r0, r1, lp string
		if err := rows.Scan(&p.ID, &p.Token0, &p.Token1, &p.PoolAddr,
			&r0, &r1, &lp, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("dex: 掃描池失敗: %w", err)
		}
		p.Reserve0, _ = new(big.Int).SetString(r0, 10)
		p.Reserve1, _ = new(big.Int).SetString(r1, 10)
		p.LPTotal, _ = new(big.Int).SetString(lp, 10)
		out = append(out, p)
	}
	return out, rows.Err()
}

// Get 讀取單一池。
func (e *Engine) Get(id string) (*Pool, error) {
	ps, err := e.Pools()
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		if p.ID == id {
			return p, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
}

// PositionOf 讀取某池某持有人持倉。
func (e *Engine) PositionOf(poolID, owner string) (*Position, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var shares string
	err := e.db.QueryRow(
		`SELECT shares FROM positions WHERE pool_id=? AND owner=?`, poolID, owner).Scan(&shares)
	if errors.Is(err, sql.ErrNoRows) {
		return &Position{PoolID: poolID, Owner: owner, Shares: big.NewInt(0)}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("dex: 讀取持倉失敗: %w", err)
	}
	s, _ := new(big.Int).SetString(shares, 10)
	return &Position{PoolID: poolID, Owner: owner, Shares: s}, nil
}

// nextPoolID 產生下一個池編號。
func (e *Engine) nextPoolID() (string, error) {
	var n int64
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM pools`).Scan(&n); err != nil {
		return "", fmt.Errorf("dex: 計數失敗: %w", err)
	}
	return fmt.Sprintf("p%d", n+1), nil
}

// CreatePool 建立一個新的流動性池（poolAddr 由 node 以 DeriveKey 派生傳入）。
func (e *Engine) CreatePool(token0, token1, poolAddr string) (*Pool, error) {
	if token0 == "" || token1 == "" {
		return nil, errors.New("dex: 代幣地址為空")
	}
	if token0 == token1 {
		return nil, ErrSameToken
	}
	if poolAddr == "" {
		return nil, errors.New("dex: 池地址為空")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	// 交易對重複檢查（順序無關）。
	rows, err := e.db.Query(`SELECT id FROM pools`)
	if err != nil {
		return nil, fmt.Errorf("dex: 查詢失敗: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("dex: 掃描失敗: %w", err)
		}
		p, err := e.getLocked(id)
		if err == nil {
			if (p.Token0 == token0 && p.Token1 == token1) ||
				(p.Token0 == token1 && p.Token1 == token0) {
				return nil, fmt.Errorf("%w: %s/%s", ErrDuplicatePool, token0, token1)
			}
		}
	}
	id, err := e.nextPoolID()
	if err != nil {
		return nil, err
	}
	now := timeNow()
	p := &Pool{
		ID: id, Token0: token0, Token1: token1, PoolAddr: poolAddr,
		Reserve0: big.NewInt(0), Reserve1: big.NewInt(0), LPTotal: big.NewInt(0),
		CreatedAt: now,
	}
	if _, err := e.db.Exec(
		`INSERT INTO pools (id, token0, token1, pool_addr, reserve0, reserve1, lp_total, created_at)
		 VALUES (?,?,?,?,?,?,?,?)`,
		p.ID, p.Token0, p.Token1, p.PoolAddr, "0", "0", "0", now); err != nil {
		return nil, fmt.Errorf("dex: 建立池失敗: %w", err)
	}
	return p, nil
}

// AddLiquidity 注入流動性：owner 將兩幣轉入池，獲得 LP 份額。
func (e *Engine) AddLiquidity(poolID, owner string, amount0, amount1 *big.Int) (*Pool, error) {
	if amount0 == nil || amount1 == nil || amount0.Sign() <= 0 || amount1.Sign() <= 0 {
		return nil, ErrBadAmount
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	p, err := e.getLocked(poolID)
	if err != nil {
		return nil, err
	}
	// 先轉入（上鏈）再讀餘額確認。
	if err := e.transfer(poolID, owner, p.PoolAddr, p.Token0, amount0); err != nil {
		return nil, err
	}
	if err := e.transfer(poolID, owner, p.PoolAddr, p.Token1, amount1); err != nil {
		return nil, err
	}
	bal0, err := e.balance(p.PoolAddr, p.Token0)
	if err != nil {
		return nil, err
	}
	bal1, err := e.balance(p.PoolAddr, p.Token1)
	if err != nil {
		return nil, err
	}
	// 首次注入：LP = sqrt(a0*a1)；後續：按「較小比例」配份額。
	var shares *big.Int
	if p.LPTotal.Sign() == 0 || p.Reserve0.Sign() == 0 || p.Reserve1.Sign() == 0 {
		shares = isqrt(new(big.Int).Mul(bal0, bal1))
		if shares.Sign() == 0 {
			return nil, errors.New("dex: 首次注入份額為 0")
		}
	} else {
		s0 := new(big.Int).Mul(bal0, p.LPTotal)
		s0.Div(s0, p.Reserve0)
		s1 := new(big.Int).Mul(bal1, p.LPTotal)
		s1.Div(s1, p.Reserve1)
		shares = s0
		if s1.Cmp(shares) < 0 {
			shares = s1
		}
		if shares.Sign() == 0 {
			return nil, errors.New("dex: 注入比例不足，份額為 0")
		}
	}
	// 更新儲備與份額。
	p.Reserve0 = bal0
	p.Reserve1 = bal1
	p.LPTotal = new(big.Int).Add(p.LPTotal, shares)
	if err := e.savePoolLocked(p); err != nil {
		return nil, err
	}
	pos, err := e.positionLocked(poolID, owner)
	if err != nil {
		return nil, err
	}
	pos.Shares = new(big.Int).Add(pos.Shares, shares)
	if err := e.savePositionLocked(pos); err != nil {
		return nil, err
	}
	return p, nil
}

// RemoveLiquidity 按份額比例退回流動性。
func (e *Engine) RemoveLiquidity(poolID, owner string, shares *big.Int) (*Pool, error) {
	if shares == nil || shares.Sign() <= 0 {
		return nil, ErrBadShare
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	p, err := e.getLocked(poolID)
	if err != nil {
		return nil, err
	}
	if p.LPTotal.Sign() == 0 {
		return nil, ErrInsufficient
	}
	pos, err := e.positionLocked(poolID, owner)
	if err != nil {
		return nil, err
	}
	if pos.Shares.Cmp(shares) < 0 {
		return nil, ErrNoPosition
	}
	// 已質押份額不可退出（流動性挖礦鎖倉）。
	spos, err := e.stakePositionLocked(poolID, owner)
	if err != nil {
		return nil, err
	}
	avail := new(big.Int).Sub(pos.Shares, spos.Shares)
	if shares.Cmp(avail) > 0 {
		return nil, fmt.Errorf("%w: 已質押 %s 份額", ErrNoPosition, spos.Shares.String())
	}
	// 比例退回。
	a0 := new(big.Int).Mul(p.Reserve0, shares)
	a0.Div(a0, p.LPTotal)
	a1 := new(big.Int).Mul(p.Reserve1, shares)
	a1.Div(a1, p.LPTotal)
	if a0.Sign() <= 0 || a1.Sign() <= 0 {
		return nil, ErrInsufficient
	}
	if err := e.transfer(poolID, p.PoolAddr, owner, p.Token0, a0); err != nil {
		return nil, err
	}
	if err := e.transfer(poolID, p.PoolAddr, owner, p.Token1, a1); err != nil {
		return nil, err
	}
	bal0, err := e.balance(p.PoolAddr, p.Token0)
	if err != nil {
		return nil, err
	}
	bal1, err := e.balance(p.PoolAddr, p.Token1)
	if err != nil {
		return nil, err
	}
	p.Reserve0 = bal0
	p.Reserve1 = bal1
	p.LPTotal = new(big.Int).Sub(p.LPTotal, shares)
	if p.LPTotal.Sign() < 0 {
		p.LPTotal = big.NewInt(0)
	}
	if err := e.savePoolLocked(p); err != nil {
		return nil, err
	}
	pos.Shares = new(big.Int).Sub(pos.Shares, shares)
	if err := e.savePositionLocked(pos); err != nil {
		return nil, err
	}
	return p, nil
}

// Quote 計算 Swap 可得的輸出數量（不執行）。
func (e *Engine) Quote(poolID, tokenIn string, amountIn *big.Int) (tokenOut string, out *big.Int, err error) {
	if amountIn == nil || amountIn.Sign() <= 0 {
		return "", nil, ErrBadAmount
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	p, err := e.getLocked(poolID)
	if err != nil {
		return "", nil, err
	}
	rIn, rOut, tOut, err := poolReservesFor(p, tokenIn)
	if err != nil {
		return "", nil, err
	}
	if rIn.Sign() <= 0 || rOut.Sign() <= 0 {
		return "", nil, ErrInsufficient
	}
	out = swapOutput(rIn, rOut, amountIn)
	if out.Sign() <= 0 {
		return "", nil, ErrInsufficient
	}
	return tOut, out, nil
}

// Swap 執行兌換：owner 轉入 tokenIn，池轉出 tokenOut（含 0.3% 手續費）。
func (e *Engine) Swap(poolID, owner, tokenIn string, amountIn, minOut *big.Int) (tokenOut string, out *big.Int, err error) {
	if amountIn == nil || amountIn.Sign() <= 0 {
		return "", nil, ErrBadAmount
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	p, err := e.getLocked(poolID)
	if err != nil {
		return "", nil, err
	}
	rIn, rOut, tOut, err := poolReservesFor(p, tokenIn)
	if err != nil {
		return "", nil, err
	}
	if rIn.Sign() <= 0 || rOut.Sign() <= 0 {
		return "", nil, ErrInsufficient
	}
	out = swapOutput(rIn, rOut, amountIn)
	if out.Sign() <= 0 {
		return "", nil, ErrInsufficient
	}
	if minOut != nil && minOut.Sign() > 0 && out.Cmp(minOut) < 0 {
		return "", nil, ErrSlippage
	}
	// 轉入再轉出（先扣後放）。
	if err := e.transfer(poolID, owner, p.PoolAddr, tokenIn, amountIn); err != nil {
		return "", nil, err
	}
	if err := e.transfer(poolID, p.PoolAddr, owner, tOut, out); err != nil {
		return "", nil, err
	}
	balIn, err := e.balance(p.PoolAddr, tokenIn)
	if err != nil {
		return "", nil, err
	}
	balOut, err := e.balance(p.PoolAddr, tOut)
	if err != nil {
		return "", nil, err
	}
	if tokenIn == p.Token0 {
		p.Reserve0, p.Reserve1 = balIn, balOut
	} else {
		p.Reserve1, p.Reserve0 = balIn, balOut
	}
	if err := e.savePoolLocked(p); err != nil {
		return "", nil, err
	}
	return tOut, out, nil
}

// swapOutput 恆定乘積輸出（含 0.3% 手續費）：in' = in*(1-fee)；
// out = rOut - (rIn*rOut)/(rIn+in')。
func swapOutput(rIn, rOut, amountIn *big.Int) *big.Int {
	fee := big.NewInt(FeeBps)
	base := big.NewInt(10000)
	inEff := new(big.Int).Mul(amountIn, new(big.Int).Sub(base, fee))
	inEff.Div(inEff, base)
	num := new(big.Int).Mul(rIn, rOut)
	den := new(big.Int).Add(rIn, inEff)
	num.Div(num, den)
	return new(big.Int).Sub(rOut, num)
}

// poolReservesFor 依 tokenIn 決定輸入/輸出儲備與輸出代幣。
func poolReservesFor(p *Pool, tokenIn string) (rIn, rOut *big.Int, tOut string, err error) {
	switch tokenIn {
	case p.Token0:
		return new(big.Int).Set(p.Reserve0), new(big.Int).Set(p.Reserve1), p.Token1, nil
	case p.Token1:
		return new(big.Int).Set(p.Reserve1), new(big.Int).Set(p.Reserve0), p.Token0, nil
	default:
		return nil, nil, "", fmt.Errorf("%w: %s", ErrUnknownToken, tokenIn)
	}
}

func (e *Engine) getLocked(id string) (*Pool, error) {
	var r0, r1, lp string
	p := &Pool{}
	err := e.db.QueryRow(
		`SELECT id, token0, token1, pool_addr, reserve0, reserve1, lp_total, created_at
		 FROM pools WHERE id=?`, id).
		Scan(&p.ID, &p.Token0, &p.Token1, &p.PoolAddr, &r0, &r1, &lp, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("dex: 讀取池失敗: %w", err)
	}
	p.Reserve0, _ = new(big.Int).SetString(r0, 10)
	p.Reserve1, _ = new(big.Int).SetString(r1, 10)
	p.LPTotal, _ = new(big.Int).SetString(lp, 10)
	return p, nil
}

func (e *Engine) savePoolLocked(p *Pool) error {
	_, err := e.db.Exec(
		`UPDATE pools SET reserve0=?, reserve1=?, lp_total=? WHERE id=?`,
		p.Reserve0.String(), p.Reserve1.String(), p.LPTotal.String(), p.ID)
	if err != nil {
		return fmt.Errorf("dex: 更新池失敗: %w", err)
	}
	return nil
}

func (e *Engine) positionLocked(poolID, owner string) (*Position, error) {
	var shares string
	err := e.db.QueryRow(
		`SELECT shares FROM positions WHERE pool_id=? AND owner=?`, poolID, owner).Scan(&shares)
	if errors.Is(err, sql.ErrNoRows) {
		return &Position{PoolID: poolID, Owner: owner, Shares: big.NewInt(0)}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("dex: 讀取持倉失敗: %w", err)
	}
	s, _ := new(big.Int).SetString(shares, 10)
	return &Position{PoolID: poolID, Owner: owner, Shares: s}, nil
}

func (e *Engine) savePositionLocked(p *Position) error {
	if p.Shares.Sign() == 0 {
		_, err := e.db.Exec(`DELETE FROM positions WHERE pool_id=? AND owner=?`, p.PoolID, p.Owner)
		if err != nil {
			return fmt.Errorf("dex: 刪除持倉失敗: %w", err)
		}
		return nil
	}
	_, err := e.db.Exec(
		`INSERT INTO positions (pool_id, owner, shares) VALUES (?,?,?)
		 ON CONFLICT(pool_id, owner) DO UPDATE SET shares=excluded.shares`,
		p.PoolID, p.Owner, p.Shares.String())
	if err != nil {
		return fmt.Errorf("dex: 儲存持倉失敗: %w", err)
	}
	return nil
}

// isqrt 整數平方根（牛頓法）。
func isqrt(n *big.Int) *big.Int {
	if n.Sign() <= 0 {
		return big.NewInt(0)
	}
	if n.Cmp(big.NewInt(1)) == 0 {
		return big.NewInt(1)
	}
	x := new(big.Int).Set(n)
	y := new(big.Int).Rsh(new(big.Int).Add(x, big.NewInt(1)), 1)
	for y.Cmp(x) < 0 {
		x.Set(y)
		y.Rsh(new(big.Int).Add(new(big.Int).Div(n, x), x), 1)
	}
	return x
}

// timeNow 回傳目前 Unix 秒（可測試替換）。
var timeNow = func() int64 { return time.Now().Unix() }
