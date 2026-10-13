package pos

import (
	"math"
	"strconv"
	"time"
)

// DistributionResult 為一次收益分發結果。
type DistributionResult struct {
	Distributed float64 `json:"distributed"`
	StakesCount int     `json:"stakes_count"`
	Duration    int64   `json:"duration"`
	Skipped     bool    `json:"skipped"`
	Message     string  `json:"message"`
}

const yearSeconds = 365 * 24 * 3600

func round8(x float64) float64 {
	return math.Round(x*1e8) / 1e8
}

// CalculateReward 計算質押收益：量 × 年化 × (持續秒/年秒)，保留 8 位。
func CalculateReward(amount float64, durationSec int64, annual float64) float64 {
	r := amount * annual * (float64(durationSec) / float64(yearSeconds))
	return round8(r)
}

// DistributeRewards 為所有活躍質押結算並分配收益（未到間隔則跳過）。
func (s *Store) DistributeRewards() (*DistributionResult, error) {
	now := time.Now().Unix()
	lastStr, err := s.getParam("last_reward_distribution", "0")
	if err != nil {
		return nil, err
	}
	last, _ := strconv.ParseInt(lastStr, 10, 64)
	if last == 0 {
		if err := s.setParam("last_reward_distribution", strconv.FormatInt(now, 10)); err != nil {
			return nil, err
		}
		return &DistributionResult{Skipped: true, Message: "首次執行，初始化時間戳"}, nil
	}

	duration := now - last
	if duration < s.cfg.RewardInterval {
		return &DistributionResult{
			Skipped: true, Duration: duration,
			Message: "未到分配間隔",
		}, nil
	}

	annualStr, _ := s.getParam("annual_yield_rate",
		strconv.FormatFloat(s.cfg.AnnualYield, 'f', -1, 64))
	annual, _ := strconv.ParseFloat(annualStr, 64)

	// 參數須在開事務前取（MaxOpenConns=1，事務內再查 s.db 會死鎖）
	trStr, err := s.getParam("total_rewards_distributed", "0")
	if err != nil {
		return nil, err
	}
	tr, _ := strconv.ParseFloat(trStr, 64)

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.Query(
		`SELECT id,uid,amount,staked_at,last_reward_at FROM stakes WHERE status='active'`)
	if err != nil {
		return nil, err
	}
	type stakeRec struct {
		id, uid      int64
		amount       float64
		staked, last int64
	}
	var recs []stakeRec
	for rows.Next() {
		var r stakeRec
		var lastReward *int64
		if err := rows.Scan(&r.id, &r.uid, &r.amount, &r.staked, &lastReward); err != nil {
			rows.Close()
			return nil, err
		}
		if lastReward != nil {
			r.last = *lastReward
		} else {
			r.last = r.staked
		}
		recs = append(recs, r)
	}
	rows.Close()

	totalDistributed := 0.0
	count := 0
	for _, r := range recs {
		dur := now - r.last
		if dur <= 0 {
			continue
		}
		rw := CalculateReward(r.amount, dur, annual)
		if rw <= 0 {
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO rewards (uid,stake_id,amount,reward_type,distributed_at)
			 VALUES (?,?,?,'staking',?)`,
			r.uid, r.id, rw, now); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(
			`UPDATE stakes SET last_reward_at=?, total_earned=total_earned+? WHERE id=?`,
			now, rw, r.id); err != nil {
			return nil, err
		}
		totalDistributed += rw
		count++
	}

	if _, err := tx.Exec(
		`INSERT OR REPLACE INTO pos_params (key,value,updated_at) VALUES (?,?,?)`,
		"total_rewards_distributed",
		strconv.FormatFloat(round8(tr+totalDistributed), 'f', -1, 64), now); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`INSERT OR REPLACE INTO pos_params (key,value,updated_at) VALUES (?,?,?)`,
		"last_reward_distribution", strconv.FormatInt(now, 10), now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &DistributionResult{
		Distributed: round8(totalDistributed),
		StakesCount: count,
		Duration:    duration,
		Message:     "收益已分配",
	}, nil
}

// GetUserRewards 返回用戶收益記錄。
func (s *Store) GetUserRewards(uid int64, limit int) ([]Reward, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT id,uid,stake_id,amount,reward_type,distributed_at,block_height
		 FROM rewards WHERE uid=? ORDER BY distributed_at DESC LIMIT ?`, uid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Reward
	for rows.Next() {
		var r Reward
		if err := rows.Scan(&r.ID, &r.UID, &r.StakeID, &r.Amount, &r.RewardType,
			&r.DistributedAt, &r.BlockHeight); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetUserTotalEarned 返回用戶累計收益。
func (s *Store) GetUserTotalEarned(uid int64) (float64, error) {
	var total float64
	err := s.db.QueryRow(
		`SELECT COALESCE(SUM(amount),0) FROM rewards WHERE uid=?`, uid).Scan(&total)
	if err != nil {
		return 0, err
	}
	return round8(total), nil
}
