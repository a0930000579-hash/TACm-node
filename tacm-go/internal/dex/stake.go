package dex

import (
	"database/sql"
	"errors"
	"fmt"
	"math/big"
)

// StakePool 為「流動性挖礦」農場：每個池一個農場，質押 LP 份額按區塊產出
// 獎勵代幣（reward_token，鏈上真實轉帳）。accPerShare 採標準農場演算法
// （Sushi/Curve 風格）：acc += rewardPerBlock*deltaBlocks/totalStaked，
// 使用者 pending = shares*acc - debt。
type StakePool struct {
	PoolID         string
	RewardToken    string
	RewardPerBlock *big.Int // 每塊獎勵（1e18 定點）
	TotalStaked    *big.Int
	AccPerShare    *big.Int // 累計每份額獎勵（1e18 定點）
	LastTick       int64    // 上次累加區塊高度
	Funded         *big.Int // 已注入金庫的獎勵總量
	CreatedAt      int64
}

// StakePosition 為一用戶在一農場的質押倉位。
type StakePosition struct {
	PoolID string
	Owner  string
	Shares *big.Int
	Debt   *big.Int // 已結算部分（shares*acc）；pending = shares*acc - debt
}

// ErrStakeExists 同一池已建立農場。
var ErrStakeExists = errors.New("dex: 該池已有農場")

// ErrStakeNoPool 池不存在或未建立農場。
var ErrStakeNoPool = errors.New("dex: 農場不存在")

// CreateStake 建立農場（每池一個；重複拒絕）。
func (e *Engine) CreateStake(poolID, rewardToken string, rewardPerBlock *big.Int) (*StakePool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.getLocked(poolID); err != nil {
		return nil, err
	}
	if rewardToken == "" || rewardPerBlock == nil || rewardPerBlock.Sign() < 0 {
		return nil, errors.New("dex: 農場參數無效")
	}
	if _, err := e.stakePoolLocked(poolID); err == nil {
		return nil, ErrStakeExists
	}
	sp := &StakePool{
		PoolID:         poolID,
		RewardToken:    rewardToken,
		RewardPerBlock: new(big.Int).Set(rewardPerBlock),
		TotalStaked:    big.NewInt(0),
		AccPerShare:    big.NewInt(0),
		LastTick:       0,
		Funded:         big.NewInt(0),
		CreatedAt:      timeNow(),
	}
	if err := e.saveStakePoolLocked(sp); err != nil {
		return nil, err
	}
	return sp, nil
}

// FundStake 由做市帳戶（admin）鏈上轉帳獎勵代幣至農場金庫，並記錄注入量。
// 金庫地址由節點以 DeriveKey("tac-dex-stake-<pool>") 確定性派生。
func (e *Engine) FundStake(poolID string, amount *big.Int) (*StakePool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	sp, err := e.stakePoolLocked(poolID)
	if err != nil {
		return nil, err
	}
	if amount == nil || amount.Sign() <= 0 {
		return nil, errors.New("dex: 注入數量無效")
	}
	if e.transfer == nil {
		return nil, errors.New("dex: 轉帳回調未注入")
	}
	// from=admin（節點金鑰）、to=農場金庫；節點依目標地址自動選簽名者。
	if err := e.transfer(poolID, e.admin, "tac-dex-stake:"+poolID, sp.RewardToken, amount); err != nil {
		return nil, err
	}
	sp.Funded = new(big.Int).Add(sp.Funded, amount)
	if err := e.saveStakePoolLocked(sp); err != nil {
		return nil, err
	}
	return sp, nil
}

// StakeTick 由節點每出塊呼叫：累加 accPerShare 至最新高度。
// 無質押時仍推進 lastTick，避免解鎖後一次性補發。
func (e *Engine) StakeTick(height int64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stakeTickLocked(height)
}

// StakeTickTry 嘗試累加農場獎勵；若引擎鎖正被「鏈上轉帳等待」持有則跳過本塊，
// 避免「出塊等引擎鎖 × 鏈上等待等出塊」的互等死鎖（tick 可延後，下塊再累加）。
func (e *Engine) StakeTickTry(height int64) error {
	if !e.mu.TryLock() {
		return nil
	}
	defer e.mu.Unlock()
	return e.stakeTickLocked(height)
}

// stakeTickLocked 累加全部農場（呼叫端須已持鎖）。
func (e *Engine) stakeTickLocked(height int64) error {
	rows, err := e.db.Query(`SELECT pool_id FROM stake_pools`)
	if err != nil {
		return fmt.Errorf("dex: 讀取農場列表失敗: %w", err)
	}
	var ids []string
	for rows.Next() {
		var pid string
		if err := rows.Scan(&pid); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, pid)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, pid := range ids {
		sp, err := e.stakePoolLocked(pid)
		if err != nil {
			continue
		}
		if err := e.tickStakeLocked(sp, height); err != nil {
			return err
		}
	}
	return nil
}

// tickStakeLocked 累加單一農場至 height。
func (e *Engine) tickStakeLocked(sp *StakePool, height int64) error {
	if height <= sp.LastTick {
		return nil
	}
	delta := height - sp.LastTick
	if sp.TotalStaked.Sign() > 0 && delta > 0 && sp.RewardPerBlock.Sign() > 0 {
		// acc += rpb*delta/totalStaked（1e18 定點，先放大再除）。
		num := new(big.Int).Mul(sp.RewardPerBlock, big.NewInt(delta))
		num = num.Mul(num, big.NewInt(1e18))
		inc := new(big.Int).Div(num, sp.TotalStaked)
		sp.AccPerShare = new(big.Int).Add(sp.AccPerShare, inc)
	}
	sp.LastTick = height
	return e.saveStakePoolLocked(sp)
}

// Stake 質押 LP 份額：需小於等於未質押餘額（Position.Shares - 已質押）。
func (e *Engine) Stake(poolID, owner string, shares *big.Int) (*StakePosition, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	sp, err := e.stakePoolLocked(poolID)
	if err != nil {
		return nil, err
	}
	if shares == nil || shares.Sign() <= 0 {
		return nil, errors.New("dex: 質押數量無效")
	}
	pos, err := e.positionLocked(poolID, owner)
	if err != nil {
		return nil, err
	}
	spos, err := e.stakePositionLocked(poolID, owner)
	if err != nil {
		return nil, err
	}
	// 可質押 = 總份額 - 已質押。
	locked := new(big.Int).Add(spos.Shares, shares)
	if locked.Cmp(pos.Shares) > 0 {
		return nil, ErrInsufficient
	}
	// 結算當下 pending（debt 跟隨 acc 移動）。
	if err := e.tickStakeLocked(sp, sp.LastTick); err != nil {
		return nil, err
	}
	spos.Debt = new(big.Int).Mul(new(big.Int).Add(spos.Shares, shares), sp.AccPerShare)
	spos.Shares = locked
	sp.TotalStaked = new(big.Int).Add(sp.TotalStaked, shares)
	if err := e.saveStakePoolLocked(sp); err != nil {
		return nil, err
	}
	if err := e.saveStakePositionLocked(spos); err != nil {
		return nil, err
	}
	return spos, nil
}

// Unstake 解除質押，退回 LP 份額。
func (e *Engine) Unstake(poolID, owner string, shares *big.Int) (*StakePosition, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	sp, err := e.stakePoolLocked(poolID)
	if err != nil {
		return nil, err
	}
	if shares == nil || shares.Sign() <= 0 {
		return nil, errors.New("dex: 解除數量無效")
	}
	spos, err := e.stakePositionLocked(poolID, owner)
	if err != nil {
		return nil, err
	}
	if shares.Cmp(spos.Shares) > 0 {
		return nil, ErrInsufficient
	}
	if err := e.tickStakeLocked(sp, sp.LastTick); err != nil {
		return nil, err
	}
	spos.Shares = new(big.Int).Sub(spos.Shares, shares)
	spos.Debt = new(big.Int).Mul(spos.Shares, sp.AccPerShare)
	sp.TotalStaked = new(big.Int).Sub(sp.TotalStaked, shares)
	if err := e.saveStakePoolLocked(sp); err != nil {
		return nil, err
	}
	if err := e.saveStakePositionLocked(spos); err != nil {
		return nil, err
	}
	return spos, nil
}

// Pending 回傳未領取獎勵：shares*acc - debt。
func (e *Engine) Pending(poolID, owner string) (*big.Int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	sp, err := e.stakePoolLocked(poolID)
	if err != nil {
		return nil, err
	}
	spos, err := e.stakePositionLocked(poolID, owner)
	if err != nil {
		return nil, err
	}
	if err := e.tickStakeLocked(sp, sp.LastTick); err != nil {
		return nil, err
	}
	p := new(big.Int).Mul(spos.Shares, sp.AccPerShare)
	p = new(big.Int).Sub(p, spos.Debt)
	// acc 為 1e18 定點：pending = (shares*acc - debt) / 1e18。
	p = new(big.Int).Div(p, big.NewInt(1e18))
	if p.Sign() < 0 {
		p = big.NewInt(0)
	}
	return p, nil
}

// Claim 領取獎勵：鏈上由農場金庫轉帳至 owner，並結算 debt。
// 金庫地址以 from="tac-dex-stake:<pool>" 標記，由節點端解析為派生簽名者。
func (e *Engine) Claim(poolID, owner string) (*big.Int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	sp, err := e.stakePoolLocked(poolID)
	if err != nil {
		return nil, err
	}
	spos, err := e.stakePositionLocked(poolID, owner)
	if err != nil {
		return nil, err
	}
	if err := e.tickStakeLocked(sp, sp.LastTick); err != nil {
		return nil, err
	}
	p := new(big.Int).Mul(spos.Shares, sp.AccPerShare)
	p = new(big.Int).Sub(p, spos.Debt)
	// acc 為 1e18 定點：實際獎勵 = (shares*acc - debt) / 1e18。
	p = new(big.Int).Div(p, big.NewInt(1e18))
	if p.Sign() <= 0 {
		return nil, errors.New("dex: 無可領取獎勵")
	}
	if p.Cmp(sp.Funded) > 0 {
		return nil, errors.New("dex: 農場獎勵資金不足")
	}
	if e.transfer == nil {
		return nil, errors.New("dex: 轉帳回調未注入")
	}
	// 金庫 → owner 鏈上轉帳。
	if err := e.transfer(poolID, "tac-dex-stake:"+poolID, owner, sp.RewardToken, p); err != nil {
		return nil, err
	}
	spos.Debt = new(big.Int).Mul(spos.Shares, sp.AccPerShare)
	sp.Funded = new(big.Int).Sub(sp.Funded, p)
	if err := e.saveStakePoolLocked(sp); err != nil {
		return nil, err
	}
	if err := e.saveStakePositionLocked(spos); err != nil {
		return nil, err
	}
	return p, nil
}

// StakePools 回傳全部農場（按建立順序）。
func (e *Engine) StakePools() ([]*StakePool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rows, err := e.db.Query(
		`SELECT pool_id, reward_token, reward_per_block, total_staked, acc_per_share,
		        last_tick, funded, created_at FROM stake_pools ORDER BY rowid`)
	if err != nil {
		return nil, fmt.Errorf("dex: 讀取農場列表失敗: %w", err)
	}
	defer rows.Close()
	var out []*StakePool
	for rows.Next() {
		sp := &StakePool{}
		var rpb, ts, acc, fd string
		if err := rows.Scan(&sp.PoolID, &sp.RewardToken, &rpb, &ts, &acc, &sp.LastTick, &fd, &sp.CreatedAt); err != nil {
			return nil, err
		}
		sp.RewardPerBlock, _ = new(big.Int).SetString(rpb, 10)
		sp.TotalStaked, _ = new(big.Int).SetString(ts, 10)
		sp.AccPerShare, _ = new(big.Int).SetString(acc, 10)
		sp.Funded, _ = new(big.Int).SetString(fd, 10)
		out = append(out, sp)
	}
	return out, rows.Err()
}

// StakePositionOf 回傳用戶質押倉位（不存在回 0 倉位）。
func (e *Engine) StakePositionOf(poolID, owner string) (*StakePosition, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stakePositionLocked(poolID, owner)
}

func (e *Engine) stakePoolLocked(poolID string) (*StakePool, error) {
	var rpb, ts, acc, fd string
	sp := &StakePool{}
	err := e.db.QueryRow(
		`SELECT pool_id, reward_token, reward_per_block, total_staked, acc_per_share,
		        last_tick, funded, created_at FROM stake_pools WHERE pool_id=?`, poolID).
		Scan(&sp.PoolID, &sp.RewardToken, &rpb, &ts, &acc, &sp.LastTick, &fd, &sp.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrStakeNoPool, poolID)
	}
	if err != nil {
		return nil, fmt.Errorf("dex: 讀取農場失敗: %w", err)
	}
	sp.RewardPerBlock, _ = new(big.Int).SetString(rpb, 10)
	sp.TotalStaked, _ = new(big.Int).SetString(ts, 10)
	sp.AccPerShare, _ = new(big.Int).SetString(acc, 10)
	sp.Funded, _ = new(big.Int).SetString(fd, 10)
	return sp, nil
}

func (e *Engine) stakePositionLocked(poolID, owner string) (*StakePosition, error) {
	var shares, debt string
	spos := &StakePosition{PoolID: poolID, Owner: owner}
	err := e.db.QueryRow(
		`SELECT shares, debt FROM stake_positions WHERE pool_id=? AND owner=?`, poolID, owner).
		Scan(&shares, &debt)
	if errors.Is(err, sql.ErrNoRows) {
		spos.Shares = big.NewInt(0)
		spos.Debt = big.NewInt(0)
		return spos, nil
	}
	if err != nil {
		return nil, fmt.Errorf("dex: 讀取質押倉位失敗: %w", err)
	}
	spos.Shares, _ = new(big.Int).SetString(shares, 10)
	spos.Debt, _ = new(big.Int).SetString(debt, 10)
	return spos, nil
}

func (e *Engine) saveStakePoolLocked(sp *StakePool) error {
	_, err := e.db.Exec(
		`INSERT INTO stake_pools (pool_id, reward_token, reward_per_block, total_staked,
		        acc_per_share, last_tick, funded, created_at)
		 VALUES (?,?,?,?,?,?,?,?)
		 ON CONFLICT(pool_id) DO UPDATE SET
		   total_staked=excluded.total_staked,
		   acc_per_share=excluded.acc_per_share,
		   last_tick=excluded.last_tick,
		   funded=excluded.funded`,
		sp.PoolID, sp.RewardToken, sp.RewardPerBlock.String(), sp.TotalStaked.String(),
		sp.AccPerShare.String(), sp.LastTick, sp.Funded.String(), sp.CreatedAt)
	if err != nil {
		return fmt.Errorf("dex: 儲存農場失敗: %w", err)
	}
	return nil
}

func (e *Engine) saveStakePositionLocked(spos *StakePosition) error {
	if spos.Shares.Sign() == 0 {
		_, err := e.db.Exec(`DELETE FROM stake_positions WHERE pool_id=? AND owner=?`, spos.PoolID, spos.Owner)
		if err != nil {
			return fmt.Errorf("dex: 刪除質押倉位失敗: %w", err)
		}
		return nil
	}
	_, err := e.db.Exec(
		`INSERT INTO stake_positions (pool_id, owner, shares, debt) VALUES (?,?,?,?)
		 ON CONFLICT(pool_id, owner) DO UPDATE SET shares=excluded.shares, debt=excluded.debt`,
		spos.PoolID, spos.Owner, spos.Shares.String(), spos.Debt.String())
	if err != nil {
		return fmt.Errorf("dex: 儲存質押倉位失敗: %w", err)
	}
	return nil
}
