package chaindb

import (
	"math"
	"os"
	"sync"
	"testing"
)

// TestAnnualDecayTotalSupply 驗證：6 年、年衰減 25%、總供應 52,003,300 時，
// 每年發行總和恰為上限（公式封閉）。
func TestAnnualDecayTotalSupply(t *testing.T) {
	os.Setenv("TACM_MAX_SUPPLY", "52003300")
	os.Setenv("TACM_EMISSION_YEARS", "6")
	os.Setenv("TACM_ANNUAL_DECAY_PCT", "0.25")
	emissionOnce = sync.Once{} // 重置載入
	cfg, err := LoadEmission(60)
	if err != nil {
		t.Fatalf("LoadEmission: %v", err)
	}
	if cfg.Model != "annual_decay" || cfg.MaxSupply != 52003300 {
		t.Fatalf("設定異常: %+v", cfg)
	}
	// 每年 = PERIOD_BLOCKS 塊；6 年總發行 = INITIAL_SUBSIDY * SUM(0.75^i, i=0..5)
	total := 0.0
	for y := 0; y < 6; y++ {
		total += float64(cfg.PeriodBlocks) * cfg.InitialSubsidy * math.Pow(cfg.DecayRatio, float64(y))
	}
	if math.Abs(total-cfg.MaxSupply) > 1e-3 {
		t.Fatalf("總發行 %f ≠ 上限 %f", total, cfg.MaxSupply)
	}
	// 第 7 年（year>=6）獎勵為 0。
	if v := EmissionAmount(6*cfg.PeriodBlocks+1, 60); v != 0 {
		t.Fatalf("發行期後獎勵應為 0: %v", v)
	}
	// 第一塊獎勵 = INITIAL_SUBSIDY。
	if v := EmissionAmount(1, 60); math.Abs(v-cfg.InitialSubsidy) > 1e-12 {
		t.Fatalf("首塊獎勵異常: %v", v)
	}
	os.Unsetenv("TACM_MAX_SUPPLY")
	os.Unsetenv("TACM_EMISSION_YEARS")
	os.Unsetenv("TACM_ANNUAL_DECAY_PCT")
	emissionOnce = sync.Once{}
}
