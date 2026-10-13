// Package pos 實現 TAC Ai 智能鏈的 PoS 質押層：質押 TACM 獲取收益、驗證人選舉、
// 解鎖期、收益按比例分配，並為三元混合共識（PoW+PoS+時間權證）提供 PoS 權重。
package pos

// Config 為 PoS 質押參數。
type Config struct {
	MinStake        float64 // 最小質押量
	MaxStake        float64 // 最大質押量
	UnlockPeriod    int64   // 解鎖期（秒）
	AnnualYield     float64 // 年化收益率
	ValidatorCount  int     // 驗證人數量
	ValidatorMinStk float64 // 成為驗證人最小質押
	RewardInterval  int64   // 收益分配間隔（秒）
	SlashRate       float64 // 罰沒率
}

// DefaultConfig 返回與系統一致的默認參數。
func DefaultConfig() Config {
	return Config{
		MinStake:        100.0,
		MaxStake:        1_000_000.0,
		UnlockPeriod:    86400 * 7,
		AnnualYield:     0.12,
		ValidatorCount:  21,
		ValidatorMinStk: 10_000.0,
		RewardInterval:  3600,
		SlashRate:       0.05,
	}
}
