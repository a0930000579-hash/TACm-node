package chaindb

import (
	"math"
	"math/big"
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

// TestHardCapBrake 驗證硬上限煞車（方案 B）：reward = min(模型獎勵, 剩餘可挖)；
// 剩餘 ≤ 0 → 0；剩餘充足 → 維持模型獎勵；nil/負已發 → 不截斷。
func TestHardCapBrake(t *testing.T) {
	const maxSupply = 52003300.0
	wei := func(x float64) *big.Int {
		return new(big.Int).Mul(big.NewInt(int64(x)), big.NewInt(1e18))
	}
	// 1) 剩餘充足：模型獎勵維持不變。
	full := 1.5045
	if got := capReward(full, maxSupply, wei(0)); math.Abs(got-full) > 1e-12 {
		t.Fatalf("剩餘充足應維持模型獎勵: %v", got)
	}
	// 2) 剩餘不足：截斷至剩餘（剩餘 = 0.5 TACm，精確 big.Int 構造）。
	mined05 := new(big.Int).Sub(wei(maxSupply), big.NewInt(500000000000000000))
	if got := capReward(full, maxSupply, mined05); math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("剩餘不足應截斷至 0.5: %v", got)
	}
	// 3) 已達上限：歸零硬停。
	if got := capReward(full, maxSupply, wei(maxSupply)); got != 0 {
		t.Fatalf("已達上限應歸零: %v", got)
	}
	if got := capReward(full, maxSupply, wei(maxSupply+100)); got != 0 {
		t.Fatalf("超過上限應歸零: %v", got)
	}
	// 4) nil 已發：不截斷（wallet 缺省時維持模型）。
	if got := capReward(full, maxSupply, nil); math.Abs(got-full) > 1e-12 {
		t.Fatalf("nil 已發應不截斷: %v", got)
	}
	// 5) MaxSupply ≤ 0（halving 無上限）：不截斷。
	if got := capReward(full, 0, wei(maxSupply*100)); math.Abs(got-full) > 1e-12 {
		t.Fatalf("無上限模式應不截斷: %v", got)
	}
}

// TestComputeCappedRewardAnnual 驗證 ComputeCappedReward 在 annual_decay 下的硬煞車整合行為。
func TestComputeCappedRewardAnnual(t *testing.T) {
	os.Setenv("TACM_MAX_SUPPLY", "52003300")
	os.Setenv("TACM_EMISSION_YEARS", "6")
	os.Setenv("TACM_ANNUAL_DECAY_PCT", "0.25")
	emissionOnce = sync.Once{}
	cfg, err := LoadEmission(3)
	if err != nil {
		t.Fatalf("LoadEmission: %v", err)
	}
	maxWei := new(big.Int).Mul(big.NewInt(int64(cfg.MaxSupply)), big.NewInt(1e18))
	// 創世後零已發：模型獎勵（首塊 InitialSubsidy）。
	want := cfg.InitialSubsidy
	if got := ComputeCappedReward(1, 3, big.NewInt(0)); math.Abs(got-want) > 1e-9 {
		t.Fatalf("零已發應為模型獎勵 %v: %v", want, got)
	}
	// 已達上限：硬停。
	if got := ComputeCappedReward(1, 3, maxWei); got != 0 {
		t.Fatalf("已達上限應歸零: %v", got)
	}
	// 剩餘 0.5 TACm：截斷至 0.5。
	remaining05 := new(big.Int).Set(maxWei)
	remaining05.Sub(remaining05, big.NewInt(int64(0.5*1e18)))
	if got := ComputeCappedReward(1, 3, remaining05); math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("剩餘 0.5 應截斷: %v", got)
	}
	os.Unsetenv("TACM_MAX_SUPPLY")
	os.Unsetenv("TACM_EMISSION_YEARS")
	os.Unsetenv("TACM_ANNUAL_DECAY_PCT")
	emissionOnce = sync.Once{}
}

// TestEmissionBlockTime12 v167：出塊間隔 3→12 秒後的供應自適應斷言。
// 關鍵：PeriodBlocks = 一年秒數 / blockTime，故 12 秒的年區塊數為 3 秒的 1/4；
// InitialSubsidy = MaxSupply/(PeriodBlocks*DecaySumFactor) 反向下放為 4 倍——
// 使「單一日曆年」總發行量不受出塊間隔影響，硬上限仍為 52,003,300。
func TestEmissionBlockTime12(t *testing.T) {
	os.Setenv("TACM_MAX_SUPPLY", "52003300")
	os.Setenv("TACM_EMISSION_YEARS", "6")
	os.Setenv("TACM_ANNUAL_DECAY_PCT", "0.25")
	defer func() {
		os.Unsetenv("TACM_MAX_SUPPLY")
		os.Unsetenv("TACM_EMISSION_YEARS")
		os.Unsetenv("TACM_ANNUAL_DECAY_PCT")
		emissionOnce = sync.Once{}
	}()

	const secPerYear = int64(365 * 24 * 3600)

	// ① blockTime=12：年區塊數 = 31,536,000/12 = 2,628,000。
	emissionOnce = sync.Once{}
	cfg12, err := LoadEmission(12)
	if err != nil {
		t.Fatalf("LoadEmission(12): %v", err)
	}
	if want := int64(2_628_000); cfg12.PeriodBlocks != want {
		t.Fatalf("blockTime=12 PeriodBlocks=%d, want %d (secPerYear/12)", cfg12.PeriodBlocks, want)
	}

	// ① blockTime=3：年區塊數 = 10,512,000；InitialSubsidy 應為 12 秒的 1/4。
	emissionOnce = sync.Once{}
	cfg3, err := LoadEmission(3)
	if err != nil {
		t.Fatalf("LoadEmission(3): %v", err)
	}
	if cfg3.PeriodBlocks != 10_512_000 {
		t.Fatalf("blockTime=3 PeriodBlocks=%d, want 10,512,000", cfg3.PeriodBlocks)
	}
	ratio := cfg12.InitialSubsidy / cfg3.InitialSubsidy
	if math.Abs(ratio-4.0) > 1e-6 {
		t.Fatalf("InitialSubsidy(12)/InitialSubsidy(3)=%f, want ≈4.0 (日曆年總發行不變)", ratio)
	}

	// ② 完整發行週期（6 年）累計 ≈ MaxSupply（封閉公式），不超上限。
	total12 := 0.0
	for y := 0; y < cfg12.EmissionYears; y++ {
		total12 += float64(cfg12.PeriodBlocks) * cfg12.InitialSubsidy * math.Pow(cfg12.DecayRatio, float64(y))
	}
	if total12 > cfg12.MaxSupply+1e-3 {
		t.Fatalf("blockTime=12 完整週期累計 %f 超過上限 %f", total12, cfg12.MaxSupply)
	}
	if math.Abs(total12-cfg12.MaxSupply) > 1e-3 {
		t.Fatalf("blockTime=12 累計 %f 應≈上限 %f", total12, cfg12.MaxSupply)
	}
	// 發行期結束後獎勵為 0（不增發）。
	emissionOnce = sync.Once{}
	LoadEmission(12)
	if v := EmissionAmount(int64(cfg12.EmissionYears)*cfg12.PeriodBlocks+1, 12); v != 0 {
		t.Fatalf("發行期後獎勵應為 0: %v", v)
	}
	// 硬煞車：已達上限時 ComputeCappedReward 歸零、不增發。
	maxWei := new(big.Int).Mul(big.NewInt(int64(cfg12.MaxSupply)), big.NewInt(1e18))
	if got := ComputeCappedReward(1, 12, maxWei); got != 0 {
		t.Fatalf("blockTime=12 已達上限應硬停歸零: %v", got)
	}
	// 剩餘充足時維持模型獎勵（= InitialSubsidy），不被誤截斷。
	if got := ComputeCappedReward(1, 12, big.NewInt(0)); math.Abs(got-cfg12.InitialSubsidy) > 1e-9 {
		t.Fatalf("blockTime=12 零已發應為 InitialSubsidy %v, got %v", cfg12.InitialSubsidy, got)
	}

	// ③ halving 模型：blockTime 不影響（塊數驅動減半），換 blockTime 回傳同一 BlockReward。
	os.Unsetenv("TACM_MAX_SUPPLY") // 切回 halving 預設
	emissionOnce = sync.Once{}
	if EmissionAmount(1, 3) != BlockReward(1) {
		t.Fatalf("halving 模型 blockTime=3 應回 BlockReward(1)")
	}
	if EmissionAmount(1, 12) != BlockReward(1) {
		t.Fatalf("halving 模型 blockTime=12 應回 BlockReward(1)（與 blockTime 無關）")
	}
}
