package pos

// Stake 為質押記錄。
type Stake struct {
	ID                int64   `json:"id"`
	UID               int64   `json:"uid"`
	Address           string  `json:"address"`
	Amount            float64 `json:"amount"`
	Status            string  `json:"status"`
	StakedAt          int64   `json:"staked_at"`
	UnlockRequestedAt *int64  `json:"unlock_requested_at"`
	UnlockedAt        *int64  `json:"unlocked_at"`
	LastRewardAt      *int64  `json:"last_reward_at"`
	TotalEarned       float64 `json:"total_earned"`
	ValidatorID       *int64  `json:"validator_id"`
	ValidatorName     string  `json:"validator_name"`
	CommissionRate    float64 `json:"commission_rate"`
}

// Validator 為驗證人節點。
type Validator struct {
	ID             int64   `json:"id"`
	UID            int64   `json:"uid"`
	Address        string  `json:"address"`
	Name           string  `json:"name"`
	TotalStake     float64 `json:"total_stake"`
	SelfStake      float64 `json:"self_stake"`
	DelegatedStake float64 `json:"delegated_stake"`
	CommissionRate float64 `json:"commission_rate"`
	Status         string  `json:"status"`
	JoinedAt       int64   `json:"joined_at"`
	JailedAt       *int64  `json:"jailed_at"`
	Uptime         float64 `json:"uptime"`
	BlocksProposed int64   `json:"blocks_proposed"`
	BlocksMissed   int64   `json:"blocks_missed"`
	PubkeyHex      string  `json:"pubkey_hex"`
}

// Reward 為收益記錄。
type Reward struct {
	ID            int64   `json:"id"`
	UID           int64   `json:"uid"`
	StakeID       *int64  `json:"stake_id"`
	Amount        float64 `json:"amount"`
	RewardType    string  `json:"reward_type"`
	DistributedAt int64   `json:"distributed_at"`
	BlockHeight   *int64  `json:"block_height"`
}

// UnlockRequest 為解鎖請求。
type UnlockRequest struct {
	ID          int64   `json:"id"`
	StakeID     int64   `json:"stake_id"`
	UID         int64   `json:"uid"`
	Amount      float64 `json:"amount"`
	RequestedAt int64   `json:"requested_at"`
	UnlockAt    int64   `json:"unlock_at"`
	Status      string  `json:"status"`
	CompletedAt *int64  `json:"completed_at"`
}

// Stats 為 PoS 全局統計。
type Stats struct {
	TotalStaked      float64 `json:"total_staked"`
	TotalStakers     int64   `json:"total_stakers"`
	TotalStakes      int64   `json:"total_stakes"`
	ActiveValidators int64   `json:"active_validators"`
	TotalRewardsDist float64 `json:"total_rewards_distributed"`
	AnnualYieldRate  float64 `json:"annual_yield_rate"`
	UnlockPeriodDays float64 `json:"unlock_period_days"`
	MinStake         float64 `json:"min_stake"`
	ValidatorCount   int     `json:"validator_count"`
}
