package pos

import (
	"math"
	"strconv"
)

// ConsensusWeights 為三元混合共識權重。
type ConsensusWeights struct {
	PowWeight     float64 `json:"pow_weight"`
	PosWeight     float64 `json:"pos_weight"`
	TimeWeight    float64 `json:"time_weight"`
	TotalWeight   float64 `json:"total_weight"`
	PosStakeRatio float64 `json:"pos_stake_ratio"`
	TimeRatio     float64 `json:"time_ratio"`
}

func round6(x float64) float64 {
	return math.Round(x*1e6) / 1e6
}

// GetPosWeight 返回地址 PoS 權重（活躍質押/全網質押，clamp 0-1）。
func (s *Store) GetPosWeight(address string) (float64, error) {
	ts, err := s.getParam("total_staked", "0")
	if err != nil {
		return 0, err
	}
	total, _ := strconv.ParseFloat(ts, 64)
	if total <= 0 {
		return 0, nil
	}
	var user float64
	if err := s.db.QueryRow(
		`SELECT COALESCE(SUM(amount),0) FROM stakes WHERE address=? AND status='active'`,
		address).Scan(&user); err != nil {
		return 0, err
	}
	return math.Min(1.0, user/total), nil
}

// GetConsensusWeights 計算三元混合共識權重：PoW 30% + PoS 40% + 時間權證 30%。
func (s *Store) GetConsensusWeights(address string, timeOnlineSec int64) (*ConsensusWeights, error) {
	posW, err := s.GetPosWeight(address)
	if err != nil {
		return nil, err
	}
	timeW := 0.0
	if timeOnlineSec > 0 {
		timeW = math.Min(1.0, float64(timeOnlineSec)/float64(yearSeconds))
	}
	powW := 1.0

	wPos := posW * 0.4
	wTime := timeW * 0.3
	wPow := powW * 0.3

	return &ConsensusWeights{
		PowWeight:     round6(wPow),
		PosWeight:     round6(wPos),
		TimeWeight:    round6(wTime),
		TotalWeight:   round6(wPow + wPos + wTime),
		PosStakeRatio: round6(posW),
		TimeRatio:     round6(timeW),
	}, nil
}

// GetStats 返回 PoS 全局統計。
func (s *Store) GetStats() (*Stats, error) {
	var stakers, stakes, activeVals int64
	if err := s.db.QueryRow(
		`SELECT COUNT(DISTINCT uid) FROM stakes WHERE status='active'`).Scan(&stakers); err != nil {
		return nil, err
	}
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM stakes WHERE status='active'`).Scan(&stakes); err != nil {
		return nil, err
	}
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM validators WHERE status='active'`).Scan(&activeVals); err != nil {
		return nil, err
	}

	totalStakedStr, _ := s.getParam("total_staked", "0")
	totalStaked, _ := strconv.ParseFloat(totalStakedStr, 64)
	totalRewardsStr, _ := s.getParam("total_rewards_distributed", "0")
	totalRewards, _ := strconv.ParseFloat(totalRewardsStr, 64)
	annualStr, _ := s.getParam("annual_yield_rate",
		strconv.FormatFloat(s.cfg.AnnualYield, 'f', -1, 64))
	annual, _ := strconv.ParseFloat(annualStr, 64)

	return &Stats{
		TotalStaked:      totalStaked,
		TotalStakers:     stakers,
		TotalStakes:      stakes,
		ActiveValidators: activeVals,
		TotalRewardsDist: round8(totalRewards),
		AnnualYieldRate:  annual,
		UnlockPeriodDays: float64(s.cfg.UnlockPeriod) / 86400,
		MinStake:         s.cfg.MinStake,
		ValidatorCount:   s.cfg.ValidatorCount,
	}, nil
}
