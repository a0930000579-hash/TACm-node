package chaindb

import (
	"math"
	"os"
	"strconv"
	"sync"
)

// EmissionConfig 為總供應上限＋年衰減模型（搬運自 Python app.py 的
// TACM_MAX_SUPPLY 機制）。設定 TACM_MAX_SUPPLY 即啟用；
// 未設定時維持現行 HalvingInterval 減半模式（相容既有測試）。
type EmissionConfig struct {
	// Model: "halving"（預設）或 "annual_decay"。
	Model string
	// MaxSupply 總供應上限（>0 且 Model=annual_decay 時生效）。
	MaxSupply float64
	// EmissionYears 發行年數（annual_decay）。
	EmissionYears int
	// AnnualDecayPct 每年衰減比例（annual_decay）。
	AnnualDecayPct float64
	// PeriodBlocks 一年區塊數 = SECONDS_PER_YEAR / blockTime。
	PeriodBlocks int64
	// InitialSubsidy 初始每塊獎勵 = MaxSupply / (PeriodBlocks * DecaySumFactor)。
	InitialSubsidy float64
	// DecayRatio = 1 - AnnualDecayPct。
	DecayRatio float64
	// DecaySumFactor = (1-r^N)/(1-r)。
	DecaySumFactor float64
}

const (
	secPerYear = int64(365 * 24 * 3600)
)

var (
	emissionOnce   sync.Once
	emissionCfg    EmissionConfig
	emissionErr    error
	emissionLoaded bool
)

func envFloat(name string, def float64) float64 {
	if v := os.Getenv(name); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// LoadEmission 依環境變數載入供應模型；blockTimeSec 為節點實際出塊間隔（秒）。
//
//	TACM_MAX_SUPPLY         總供應上限（設定即啟用 annual_decay）
//	TACM_EMISSION_YEARS     發行年數（預設 6）
//	TACM_ANNUAL_DECAY_PCT   每年衰減（預設 0.25）
func LoadEmission(blockTimeSec int64) (EmissionConfig, error) {
	emissionOnce.Do(func() {
		maxSupply := envFloat("TACM_MAX_SUPPLY", 0)
		if maxSupply <= 0 {
			emissionCfg = EmissionConfig{Model: "halving"}
			emissionLoaded = true
			return
		}
		years := envInt("TACM_EMISSION_YEARS", 6)
		decayPct := envFloat("TACM_ANNUAL_DECAY_PCT", 0.25)
		if years < 1 {
			emissionErr = &EmissionError{"TACM_EMISSION_YEARS 至少 1"}
			return
		}
		ratio := math.Max(0.0, math.Min(1.0, 1.0-decayPct))
		var sumFactor float64
		if ratio >= 1.0 {
			sumFactor = float64(years)
		} else {
			sumFactor = (1 - math.Pow(ratio, float64(years))) / (1 - ratio)
		}
		bt := blockTimeSec
		if bt <= 0 {
			bt = 1
		}
		period := secPerYear / bt
		if period < 1 {
			period = 1
		}
		initial := maxSupply / (float64(period) * sumFactor)
		emissionCfg = EmissionConfig{
			Model:          "annual_decay",
			MaxSupply:      maxSupply,
			EmissionYears:  years,
			AnnualDecayPct: decayPct,
			PeriodBlocks:   period,
			InitialSubsidy: initial,
			DecayRatio:     ratio,
			DecaySumFactor: sumFactor,
		}
		emissionLoaded = true
	})
	if emissionErr != nil {
		return emissionCfg, emissionErr
	}
	return emissionCfg, nil
}

// Emission 回傳目前供應設定（未載入時回傳 halving 預設）。
func Emission() EmissionConfig {
	cfg, _ := LoadEmission(1)
	return cfg
}

// EmissionError 描述供應模型設定錯誤。
type EmissionError struct{ msg string }

func (e *EmissionError) Error() string { return e.msg }

// EmissionAmount 依高度計算本塊增發量（TACm）。
//
//	annual_decay：INITIAL_SUBSIDY * DecayRatio^year；發行期結束後為 0。
//	halving：沿用 BlockReward（每 HalvingInterval 減半）。
func EmissionAmount(height int64, blockTimeSec int64) float64 {
	cfg, err := LoadEmission(blockTimeSec)
	if err != nil {
		return 0.0
	}
	if cfg.Model == "annual_decay" {
		if height <= 0 {
			return 0.0
		}
		yearIdx := height / cfg.PeriodBlocks
		if yearIdx >= int64(cfg.EmissionYears) {
			return 0.0
		}
		return cfg.InitialSubsidy * math.Pow(cfg.DecayRatio, float64(yearIdx))
	}
	return BlockReward(height)
}
