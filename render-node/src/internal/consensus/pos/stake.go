package pos

import (
	"database/sql"
	"fmt"
	"strconv"
	"time"
)

const stakeSelect = `SELECT s.id, s.uid, s.address, s.amount, s.status, s.staked_at,
	s.unlock_requested_at, s.unlocked_at, s.last_reward_at, s.total_earned,
	s.validator_id, v.name, v.commission_rate
	FROM stakes s LEFT JOIN validators v ON s.validator_id = v.id`

func scanStake(scanner interface{ Scan(...any) error }) (*Stake, error) {
	var st Stake
	var vname sql.NullString
	var commission sql.NullFloat64
	err := scanner.Scan(
		&st.ID, &st.UID, &st.Address, &st.Amount, &st.Status, &st.StakedAt,
		&st.UnlockRequestedAt, &st.UnlockedAt, &st.LastRewardAt, &st.TotalEarned,
		&st.ValidatorID, &vname, &commission)
	if err != nil {
		return nil, err
	}
	st.ValidatorName = vname.String
	st.CommissionRate = commission.Float64
	return &st, nil
}

// Stake 質押 TACM（可追加同驗證人的活躍質押）。
func (s *Store) Stake(uid int64, address string, amount float64, validatorID *int64) (*Stake, error) {
	if amount < s.cfg.MinStake {
		return nil, fmt.Errorf("最小質押量為 %g TACM", s.cfg.MinStake)
	}
	if amount > s.cfg.MaxStake {
		return nil, fmt.Errorf("最大質押量為 %g TACM", s.cfg.MaxStake)
	}
	now := time.Now().Unix()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var stakeID int64
	var existingAmt float64
	var qerr error
	if validatorID == nil {
		qerr = tx.QueryRow(
			`SELECT id, amount FROM stakes WHERE uid=? AND status='active' AND validator_id IS NULL`,
			uid).Scan(&stakeID, &existingAmt)
	} else {
		qerr = tx.QueryRow(
			`SELECT id, amount FROM stakes WHERE uid=? AND status='active' AND validator_id=?`,
			uid, *validatorID).Scan(&stakeID, &existingAmt)
	}

	switch qerr {
	case nil:
		if _, err := tx.Exec(
			`UPDATE stakes SET amount=?, last_reward_at=? WHERE id=?`,
			existingAmt+amount, now, stakeID); err != nil {
			return nil, err
		}
	case sql.ErrNoRows:
		res, err := tx.Exec(
			`INSERT INTO stakes (uid,address,amount,status,staked_at,last_reward_at,validator_id)
			 VALUES (?,?,?,'active',?,?,?)`,
			uid, address, amount, now, now, validatorID)
		if err != nil {
			return nil, err
		}
		stakeID, _ = res.LastInsertId()
	default:
		return nil, qerr
	}

	if validatorID != nil {
		if _, err := tx.Exec(
			`UPDATE validators SET delegated_stake=delegated_stake+?, total_stake=total_stake+? WHERE id=?`,
			amount, amount, *validatorID); err != nil {
			return nil, err
		}
	} else {
		// M87：自質押若該地址已註冊驗證人，同步計入 self_stake / total_stake
		// （無匹配驗證人時為普通質押，忽略）。
		if _, err := tx.Exec(
			`UPDATE validators SET self_stake=self_stake+?, total_stake=total_stake+? WHERE address=?`,
			amount, amount, address); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	if err := s.addTotalStaked(amount); err != nil {
		return nil, err
	}
	return s.GetStakeInfo(stakeID)
}

func (s *Store) addTotalStaked(delta float64) error {
	cur, err := s.getParam("total_staked", "0")
	if err != nil {
		return err
	}
	total, _ := strconv.ParseFloat(cur, 64)
	return s.setParam("total_staked", strconv.FormatFloat(total+delta, 'f', -1, 64))
}

// RequestUnlock 申請解押，進入解鎖期；amount 為 nil 表示全額。
func (s *Store) RequestUnlock(uid, stakeID int64, amount *float64) (*UnlockRequest, error) {
	now := time.Now().Unix()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var stAmt float64
	var stStatus string
	err = tx.QueryRow(`SELECT amount,status FROM stakes WHERE id=? AND uid=?`, stakeID, uid).
		Scan(&stAmt, &stStatus)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("質押記錄不存在")
	}
	if err != nil {
		return nil, err
	}
	if stStatus != "active" {
		return nil, fmt.Errorf("質押狀態為 %s，無法解押", stStatus)
	}

	unlockAmount := stAmt
	if amount != nil {
		unlockAmount = *amount
	}
	if unlockAmount > stAmt {
		return nil, fmt.Errorf("解押金額超過質押金額")
	}
	unlockAt := now + s.cfg.UnlockPeriod

	res, err := tx.Exec(
		`INSERT INTO unlock_requests (stake_id,uid,amount,requested_at,unlock_at,status)
		 VALUES (?,?,?,?,?,'pending')`,
		stakeID, uid, unlockAmount, now, unlockAt)
	if err != nil {
		return nil, err
	}
	reqID, _ := res.LastInsertId()

	remaining := stAmt - unlockAmount
	if remaining <= 0 {
		if _, err := tx.Exec(
			`UPDATE stakes SET status='unlocking', unlock_requested_at=? WHERE id=?`,
			now, stakeID); err != nil {
			return nil, err
		}
	} else {
		if _, err := tx.Exec(`UPDATE stakes SET amount=? WHERE id=?`, remaining, stakeID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.getUnlockRequest(reqID)
}

// CompleteUnlock 解鎖期結束後完成解鎖，返回退回金額。
func (s *Store) CompleteUnlock(requestID int64) (float64, error) {
	now := time.Now().Unix()

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var stakeID int64
	var amount float64
	var unlockAt int64
	var status string
	err = tx.QueryRow(
		`SELECT stake_id,amount,unlock_at,status FROM unlock_requests WHERE id=?`,
		requestID).Scan(&stakeID, &amount, &unlockAt, &status)
	if err == sql.ErrNoRows {
		return 0, fmt.Errorf("解鎖請求不存在")
	}
	if err != nil {
		return 0, err
	}
	if status != "pending" {
		return 0, fmt.Errorf("解鎖請求狀態為 %s", status)
	}
	if now < unlockAt {
		return 0, fmt.Errorf("解鎖期未結束，還需 %.1f 小時", float64(unlockAt-now)/3600)
	}

	if _, err := tx.Exec(
		`UPDATE unlock_requests SET status='completed', completed_at=? WHERE id=?`,
		now, requestID); err != nil {
		return 0, err
	}
	var remaining float64
	if err := tx.QueryRow(`SELECT amount FROM stakes WHERE id=?`, stakeID).Scan(&remaining); err != nil {
		if err == sql.ErrNoRows {
			remaining = 0
		} else {
			return 0, err
		}
	}
	if remaining <= 0 {
		if _, err := tx.Exec(
			`UPDATE stakes SET status='unlocked', unlocked_at=? WHERE id=?`,
			now, stakeID); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}

	cur, _ := s.getParam("total_staked", "0")
	total, _ := strconv.ParseFloat(cur, 64)
	if total-amount < 0 {
		total = 0
	} else {
		total -= amount
	}
	if err := s.setParam("total_staked", strconv.FormatFloat(total, 'f', -1, 64)); err != nil {
		return 0, err
	}
	return amount, nil
}

// GetStakeInfo 返回單條質押（含驗證人名）。
func (s *Store) GetStakeInfo(id int64) (*Stake, error) {
	return scanStake(s.db.QueryRow(stakeSelect+` WHERE s.id=?`, id))
}

// GetUserStakes 返回用戶全部質押。
func (s *Store) GetUserStakes(uid int64) ([]Stake, error) {
	rows, err := s.db.Query(stakeSelect+` WHERE s.uid=? ORDER BY s.staked_at DESC`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Stake
	for rows.Next() {
		st, err := scanStake(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *st)
	}
	return out, rows.Err()
}

func (s *Store) getUnlockRequest(id int64) (*UnlockRequest, error) {
	var u UnlockRequest
	err := s.db.QueryRow(
		`SELECT id,stake_id,uid,amount,requested_at,unlock_at,status,completed_at
		 FROM unlock_requests WHERE id=?`, id).Scan(
		&u.ID, &u.StakeID, &u.UID, &u.Amount, &u.RequestedAt, &u.UnlockAt, &u.Status, &u.CompletedAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}
