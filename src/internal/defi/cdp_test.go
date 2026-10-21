package defi

import (
	"math"
	"testing"
	"time"
)

// newCDPTestStore 建測試用 defi Store。
func newCDPTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// TestCDPOpenRatio 開倉抵押率校驗（M149s：DAI 式超額抵押 150%）。
func TestCDPOpenRatio(t *testing.T) {
	st := newCDPTestStore(t)
	// 抵押 200 TACm × 價格 2 USD = 400 USD；借 200 TiUSD → 比率 2.0 通過。
	id, err := st.OpenCDP("addr1", 200, 200, 2.0)
	if err != nil {
		t.Fatalf("合法開倉失敗: %v", err)
	}
	c, err := st.GetCDP(id)
	if err != nil {
		t.Fatal(err)
	}
	if c.Owner != "addr1" || c.Collateral != 200 || c.Debt != 200 {
		t.Fatalf("倉位內容異常: %+v", c)
	}
	// 比率不足：抵押 100 TACm × 2 = 200 USD，借 200 → 比率 1.0 < 1.5 拒絕。
	if _, err := st.OpenCDP("addr1", 100, 200, 2.0); err == nil {
		t.Fatal("抵押率不足應拒絕開倉")
	}
	// 價格未配置：priceUSD<=0 拒絕（真實化：不允許假價格）。
	if _, err := st.OpenCDP("addr1", 200, 100, 0); err == nil {
		t.Fatal("價格未配置應拒絕開倉")
	}
	// 邊界：恰 150% 通過。
	if _, err := st.OpenCDP("addr2", 150, 200, 2.0); err != nil {
		t.Fatalf("恰 150%% 應通過: %v", err)
	}
}

// TestCDPWithdrawCollateral 取回抵押品後比率校驗。
func TestCDPWithdrawCollateral(t *testing.T) {
	st := newCDPTestStore(t)
	id, err := st.OpenCDP("addr1", 200, 200, 2.0) // 比率 2.0
	if err != nil {
		t.Fatal(err)
	}
	// 取回 100 → 剩 100×2/200 = 1.0 < 1.5 拒絕。
	if err := st.WithdrawCollateral(id, 100, 2.0); err == nil {
		t.Fatal("取回後比率不足應拒絕")
	}
	// 取回 40 → 剩 160×2/200 = 1.6 ≥ 1.5 通過。
	if err := st.WithdrawCollateral(id, 40, 2.0); err != nil {
		t.Fatalf("合法取回失敗: %v", err)
	}
	c, _ := st.GetCDP(id)
	if math.Abs(c.Collateral-160) > 1e-9 {
		t.Fatalf("取回後抵押品應為 160, got %v", c.Collateral)
	}
}

// TestCDPStabilityFee 穩定費計提（年息 2%）。
func TestCDPStabilityFee(t *testing.T) {
	st := newCDPTestStore(t)
	id, err := st.OpenCDP("addr1", 200, 200, 2.0)
	if err != nil {
		t.Fatal(err)
	}
	// 開倉後 1 年：費 = 200 × 2% × 1 = 4。
	if err := st.AccrueStabilityFee(id, time.Now().Unix()+365*86400); err != nil {
		t.Fatal(err)
	}
	c, _ := st.GetCDP(id)
	if math.Abs(c.FeeAccrued-4) > 1e-6 {
		t.Fatalf("一年穩定費應為 4, got %v", c.FeeAccrued)
	}
	if math.Abs(c.Debt-204) > 1e-6 {
		t.Fatalf("一年後債務應為 204, got %v", c.Debt)
	}
}

// TestCDPRepay 償還（先抵穩定費再抵本金；全償自動平倉）。
func TestCDPRepay(t *testing.T) {
	st := newCDPTestStore(t)
	id, err := st.OpenCDP("addr1", 200, 200, 2.0)
	if err != nil {
		t.Fatal(err)
	}
	// 計一年費 → 債務 204。
	if err := st.AccrueStabilityFee(id, time.Now().Unix()+365*86400); err != nil {
		t.Fatal(err)
	}
	// 還 100 → 剩 104。
	remain, paid, err := st.Repay(id, 100)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(remain-104) > 1e-6 || math.Abs(paid-100) > 1e-6 {
		t.Fatalf("償還後剩餘 %v 應 104, 償還 %v", remain, paid)
	}
	// 還 104 → 全償，狀態 closed。
	remain2, _, err := st.Repay(id, 104)
	if err != nil {
		t.Fatal(err)
	}
	if remain2 != 0 {
		t.Fatalf("全償後剩餘應 0, got %v", remain2)
	}
	c, _ := st.GetCDP(id)
	if c.Status != CDPStatusClosed {
		t.Fatalf("全償後狀態應 closed, got %s", c.Status)
	}
}

// TestCDPLiquidation 清算：跌破 130% 可清算；清算人拿抵押×罰金。
func TestCDPLiquidation(t *testing.T) {
	st := newCDPTestStore(t)
	// 抵押 200、借 200、價 2 → 比率 2.0。
	id, err := st.OpenCDP("addr1", 200, 200, 2.0)
	if err != nil {
		t.Fatal(err)
	}
	// 價格跌到 1.2：比率 = 200×1.2/200 = 1.2 < 1.3 可清算。
	liq, err := st.Liquidatable(id, 1.2)
	if err != nil {
		t.Fatal(err)
	}
	if !liq {
		t.Fatal("比率 1.2 應可清算")
	}
	// 價格 1.5：比率 = 1.5 ≥ 1.3 不可清算。
	liq2, _ := st.Liquidatable(id, 1.5)
	if liq2 {
		t.Fatal("比率 1.5 不應可清算")
	}
	// 清算：債務 200（無費）；清算人可拿 = 200×1.05/1.2 = 175（< 200 抵押品）。
	seizable, debt, err := st.Liquidate(id, 1.2)
	if err != nil {
		t.Fatalf("清算失敗: %v", err)
	}
	if math.Abs(debt-200) > 1e-6 {
		t.Fatalf("清算債務應 200, got %v", debt)
	}
	if math.Abs(seizable-175) > 1e-6 {
		t.Fatalf("清算可拿應 175, got %v", seizable)
	}
	c, _ := st.GetCDP(id)
	if c.Status != CDPStatusLiq {
		t.Fatalf("清算後狀態應 liquidated, got %s", c.Status)
	}
	// 已清算倉位不可再清算。
	if _, _, err := st.Liquidate(id, 1.2); err == nil {
		t.Fatal("已清算倉位不可再清算")
	}
}

// TestCDPMarketView 市場總覽與可清算數。
func TestCDPMarketView(t *testing.T) {
	st := newCDPTestStore(t)
	if _, err := st.OpenCDP("addr1", 200, 200, 2.0); err != nil {
		t.Fatal(err)
	}
	// 健康倉位（比率 2.0）。
	if _, err := st.OpenCDP("addr2", 400, 200, 2.0); err != nil {
		t.Fatal(err)
	}
	// 價格 1.2：addr1 比率 1.2 可清算、addr2 比率 2.4 健康。
	m, err := st.CDPMarketView(1.2)
	if err != nil {
		t.Fatal(err)
	}
	if m.OpenPositions != 2 || m.Liquidatable != 1 {
		t.Fatalf("市場統計異常: %+v", m)
	}
	if math.Abs(m.TotalCollateral-600) > 1e-9 {
		t.Fatalf("總抵押應 600, got %v", m.TotalCollateral)
	}
	if !m.PriceConfigured {
		t.Fatal("價格已配置應為 true")
	}
	// 價格未配置：市場標記 price_configured=false。
	m2, _ := st.CDPMarketView(0)
	if m2.PriceConfigured {
		t.Fatal("價格未配置應標記 false")
	}
}
