package chaindb

// M156 鏈上質押委託（Staking Delegation）：持幣者將 TACm 委託給驗證人，
// 增加驗證人出塊權重（total_stake），委託紀錄隨區塊重放全網一致。
// 交易 memo 格式：pos:delegate:<delegator>:<validator> / pos:undelegate:<delegator>:<validator>
// （from 為節點代簽位址，委託人與驗證人由 memo 解析——與 pos:stake 同風格）。

import (
	"database/sql"
	"fmt"
	"strings"
)

const (
	DelegationMemo   = "pos:delegate"
	UndelegationMemo = "pos:undelegate"
)

// Unbonding 解綁中紀錄（M159：撤銷委託後鎖倉 UnbondingPeriod 區塊）。
type Unbonding struct {
	Delegator      string  `json:"delegator"`
	Validator      string  `json:"validator"`
	Amount         float64 `json:"amount"`
	Height         int64   `json:"height"`
	CompleteHeight int64   `json:"complete_height"`
	Status         string  `json:"status"`
}

// Delegation 鏈上委託紀錄（delegations 表，delegator×validator 唯一）。
type Delegation struct {
	Delegator string  `json:"delegator"`
	Validator string  `json:"validator"`
	Amount    float64 `json:"amount"`
	Ts        int64   `json:"ts"`
}

// ParseDelegationMemo 解析委託/撤銷 memo：pos:delegate:<delegator>:<validator>。
// 回傳 (delegator, validator, ok)；格式不符回 ok=false（不中斷重放）。
func ParseDelegationMemo(memo string) (string, string, bool) {
	parts := strings.SplitN(memo, ":", 4)
	if len(parts) != 4 {
		return "", "", false
	}
	if parts[0] != "pos" || (parts[1] != "delegate" && parts[1] != "undelegate") {
		return "", "", false
	}
	return parts[2], parts[3], true
}

// applyDelegationTx 區塊重放：累計委託金額（delegations 表）＋同步累計驗證人權重
// （pos_validators.total_stake，與自有質押合併為出塊權重）。
func applyDelegationTx(tx *sql.Tx, delegator, validator string, amount float64, ts int64) error {
	if amount <= 0 {
		return nil
	}
	if _, err := tx.Exec(`INSERT INTO delegations (delegator, validator, amount, ts)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(delegator, validator) DO UPDATE SET
			amount = amount + excluded.amount,
			ts = excluded.ts`, delegator, validator, amount, ts); err != nil {
		return fmt.Errorf("chaindb: 累計委託失敗: %w", err)
	}
	// 委託增加驗證人出塊權重（total_stake 合併自有質押＋委託）。
	if _, err := tx.Exec(`INSERT INTO pos_validators (address, total_stake, pubkey, status, first_seen, last_active)
		VALUES (?, ?, '', 'active', ?, ?)
		ON CONFLICT(address) DO UPDATE SET
			total_stake = total_stake + excluded.total_stake,
			last_active = excluded.last_active`,
		validator, amount, ts, ts); err != nil {
		return fmt.Errorf("chaindb: 委託權重入驗證人失敗: %w", err)
	}
	return nil
}

// applyUndelegationTx 區塊重放：撤銷委託（扣減；不足則拒絕不扣成負數）。
// applyUndelegationTx 區塊重放：撤銷委託（扣減委託與權重）並進入解綁佇列
// （M159：資產鎖倉 UnbondingPeriod 區塊，期滿由 pos:unbond_pay 返還）。
func applyUndelegationTx(tx *sql.Tx, delegator, validator string, amount float64, ts, height int64) error {
	if amount <= 0 {
		return nil
	}
	var cur float64
	err := tx.QueryRow(`SELECT amount FROM delegations WHERE delegator = ? AND validator = ?`, delegator, validator).Scan(&cur)
	if err != nil {
		return fmt.Errorf("chaindb: 撤銷委託查無紀錄: %w", err)
	}
	if amount > cur {
		return fmt.Errorf("chaindb: 撤銷委託超額（委託 %g，撤銷 %g）", cur, amount)
	}
	if _, err := tx.Exec(`UPDATE delegations SET amount = amount - ?, ts = ? WHERE delegator = ? AND validator = ?`,
		amount, ts, delegator, validator); err != nil {
		return fmt.Errorf("chaindb: 扣減委託失敗: %w", err)
	}
	// 同步扣減驗證人權重。
	if _, err := tx.Exec(`UPDATE pos_validators SET total_stake = MAX(0, total_stake - ?), last_active = ? WHERE address = ?`,
		amount, ts, validator); err != nil {
		return fmt.Errorf("chaindb: 扣減驗證人權重失敗: %w", err)
	}
	// M159：進入解綁佇列（complete_height = 當前高度 + UnbondingPeriod）。
	if _, err := tx.Exec(`INSERT INTO unbondings (delegator, validator, amount, height, complete_height, status)
		VALUES (?, ?, ?, ?, ?, 'pending')`,
		delegator, validator, amount, height, height+UnbondingPeriod); err != nil {
		return fmt.Errorf("chaindb: 寫入解綁佇列失敗: %w", err)
	}
	return nil
}

// applyUnbondPay 區塊重放：解綁期滿返還——由出塊者構造的 pos:unbond_pay 交易
// （from 空）觸發。驗證鏈上確有該 pending 解綁紀錄（防偽造）→ 扣減質押池
// 鎖倉餘額（入帳方 delegator 由 updateBalance 完成）→ 標記 paid。
func applyUnbondPay(tx *sql.Tx, delegator, validator string, amount float64, height int64) error {
	if amount <= 0 {
		return nil
	}
	var st string
	var got float64
	err := tx.QueryRow(`SELECT status, amount FROM unbondings
		WHERE delegator = ? AND validator = ? AND complete_height <= ? AND status = 'pending'
		ORDER BY height LIMIT 1`, delegator, validator, height).Scan(&st, &got)
	if err != nil {
		return fmt.Errorf("chaindb: 解綁返還查無待返還紀錄: %w", err)
	}
	if amount > got+1e-9 {
		return fmt.Errorf("chaindb: 解綁返還金額超額（待返還 %g，交易 %g）", got, amount)
	}
	// 扣減質押池鎖倉餘額（鏈上帳戶一致；不足則回滾保持全網一致）。
	if _, err := tx.Exec(`UPDATE accounts SET balance = MAX(0, balance - ?) WHERE address = ?`,
		amount, PosStakePoolAddr); err != nil {
		return fmt.Errorf("chaindb: 扣減質押池失敗: %w", err)
	}
	if _, err := tx.Exec(`UPDATE unbondings SET status = 'paid' WHERE delegator = ? AND validator = ? AND complete_height <= ? AND status = 'pending'`,
		delegator, validator, height); err != nil {
		return fmt.Errorf("chaindb: 標記解綁已返還失敗: %w", err)
	}
	return nil
}

// GetUnbondings 返回委託人全部解綁中紀錄（pending）。
func (c *ChainDB) GetUnbondings(delegator string) ([]Unbonding, error) {
	rows, err := c.db.Query(`SELECT delegator, validator, amount, height, complete_height, status FROM unbondings
		WHERE delegator = ? ORDER BY height`, delegator)
	if err != nil {
		return nil, fmt.Errorf("chaindb: 讀解綁佇列失敗: %w", err)
	}
	defer rows.Close()
	var out []Unbonding
	for rows.Next() {
		var u Unbonding
		if err := rows.Scan(&u.Delegator, &u.Validator, &u.Amount, &u.Height, &u.CompleteHeight, &u.Status); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}


// GetDelegations 返回委託人全部委託（任何節點同步同鏈結果一致）。
func (c *ChainDB) GetDelegations(delegator string) ([]Delegation, error) {
	rows, err := c.db.Query(`SELECT delegator, validator, amount, ts FROM delegations
		WHERE delegator = ? AND amount > 0 ORDER BY ts DESC`, delegator)
	if err != nil {
		return nil, fmt.Errorf("chaindb: 讀委託失敗: %w", err)
	}
	defer rows.Close()
	var out []Delegation
	for rows.Next() {
		var d Delegation
		if err := rows.Scan(&d.Delegator, &d.Validator, &d.Amount, &d.Ts); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetValidatorDelegations 返回驗證人收到之全部委託（含委託人與金額）。
func (c *ChainDB) GetValidatorDelegations(validator string) ([]Delegation, error) {
	rows, err := c.db.Query(`SELECT delegator, validator, amount, ts FROM delegations
		WHERE validator = ? AND amount > 0 ORDER BY amount DESC`, validator)
	if err != nil {
		return nil, fmt.Errorf("chaindb: 讀驗證人委託失敗: %w", err)
	}
	defer rows.Close()
	var out []Delegation
	for rows.Next() {
		var d Delegation
		if err := rows.Scan(&d.Delegator, &d.Validator, &d.Amount, &d.Ts); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetDelegationTotal 返回單一委託人對單一驗證人之委託額（無紀錄=0）。
func (c *ChainDB) GetDelegationTotal(delegator, validator string) (float64, error) {
	var n float64
	if err := c.db.QueryRow(`SELECT COALESCE(amount, 0) FROM delegations
		WHERE delegator = ? AND validator = ?`, delegator, validator).Scan(&n); err != nil {
		return 0, fmt.Errorf("chaindb: 讀委託額失敗: %w", err)
	}
	return n, nil
}

// CountDelegations 統計委託關係筆數（索引 API 可擴充用）。
func (c *ChainDB) CountDelegations() (int64, error) {
	var n int64
	if err := c.db.QueryRow(`SELECT COUNT(*) FROM delegations WHERE amount > 0`).Scan(&n); err != nil {
		return 0, fmt.Errorf("chaindb: 統計委託失敗: %w", err)
	}
	return n, nil
}


// UnbondingMemo 委託解綁完成返還交易 memo 標記（M159）：由出塊節點於
// complete_height 到達後自動構造（from 空、無簽名），重放時返還質押池鎖倉資產。
const UnbondingMemo = "pos:unbond_pay"

// UnbondingPeriod 委託解綁定時限（區塊數）：撤銷委託後資產鎖倉該高度數，
// 期間驗證人如有惡意（slash）可追討；期滿由節點自動返還。全網固定常量。
const UnbondingPeriod = int64(100)

// DelegRewardMemo 委託獎勵 coinbase 交易 memo 標記（M157：節點獎勵委託拆分）。
const DelegRewardMemo = "coinbase:deleg"

// GetDelegationRewardTotal 返回委託者累計已獲得之委託獎勵（鏈上 coinbase:deleg 入賬總額）。
func (c *ChainDB) GetDelegationRewardTotal(delegator string) (float64, error) {
	var n float64
	if err := c.db.QueryRow(`SELECT COALESCE(SUM(CAST(amount AS REAL)), 0) FROM transactions
		WHERE from_addr = '' AND memo = ? AND to_addr = ?`, DelegRewardMemo, delegator).Scan(&n); err != nil {
		return 0, fmt.Errorf("chaindb: 讀委託獎勵失敗: %w", err)
	}
	return n, nil
}

// CountDelegationRewards 統計委託獎勵交易筆數。
func (c *ChainDB) CountDelegationRewards() (int64, error) {
	var n int64
	if err := c.db.QueryRow(`SELECT COUNT(*) FROM transactions
		WHERE from_addr = '' AND memo = ?`, DelegRewardMemo).Scan(&n); err != nil {
		return 0, fmt.Errorf("chaindb: 統計委託獎勵失敗: %w", err)
	}
	return n, nil
}

// ListDelegations 全鏈委託紀錄分頁（amount 降序，限額由呼叫方控制）。
func (c *ChainDB) ListDelegations(limit, offset int) ([]Delegation, error) {
	rows, err := c.db.Query(`SELECT delegator, validator, amount, ts FROM delegations
		WHERE amount > 0 ORDER BY amount DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("chaindb: 列委託失敗: %w", err)
	}
	defer rows.Close()
	var out []Delegation
	for rows.Next() {
		var d Delegation
		if err := rows.Scan(&d.Delegator, &d.Validator, &d.Amount, &d.Ts); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ListPendingUnbondings 返回截至指定高度應返還之解綁紀錄（status='pending' 且 complete_height≤height）。
func (c *ChainDB) ListPendingUnbondings(height int64) ([]Unbonding, error) {
	rows, err := c.db.Query(`SELECT delegator, validator, amount, height, complete_height, status FROM unbondings
		WHERE status = 'pending' AND complete_height <= ? ORDER BY height`, height)
	if err != nil {
		return nil, fmt.Errorf("chaindb: 讀待返還解綁失敗: %w", err)
	}
	defer rows.Close()
	var out []Unbonding
	for rows.Next() {
		var u Unbonding
		if err := rows.Scan(&u.Delegator, &u.Validator, &u.Amount, &u.Height, &u.CompleteHeight, &u.Status); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
