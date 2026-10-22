package dexv3

// dexv3 套件級單元測試：不依賴 node，直接測 Store 與集中流動性 swap 數學。

import (
	"math"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func almostEq(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestCreatePoolValidation(t *testing.T) {
	st := newTestStore(t)

	// token 自動排序（TIUSD 字典序大於 TACm → token0=TACm）。
	p, err := st.CreatePool("TIUSD", "TACm", 30, 1.0)
	if err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	if p.Token0 != "TACm" || p.Token1 != "TIUSD" {
		t.Fatalf("token 排序錯誤: %s/%s", p.Token0, p.Token1)
	}
	if p.TickSpacing != 60 {
		t.Fatalf("feeTier=30 應 spacing=60, got %d", p.TickSpacing)
	}
	if p.Tick != 0 || !almostEq(p.SqrtPrice, 1.0, 1e-9) {
		t.Fatalf("初始 tick/sqrtPrice 錯誤: tick=%d sqrt=%.6f", p.Tick, p.SqrtPrice)
	}

	// 重複池應失敗。
	if _, err := st.CreatePool("TACm", "TIUSD", 30, 1.0); err == nil {
		t.Fatal("重複池應失敗")
	}
	// 非法費率。
	if _, err := st.CreatePool("TACm", "TIUSD", 100, 1.0); err == nil {
		t.Fatal("非法費率應失敗")
	}
	// 不同費率分級可並存。
	if _, err := st.CreatePool("TACm", "TIUSD", 5, 1.0); err != nil {
		t.Fatalf("feeTier=5 池應可建立: %v", err)
	}
}

func TestMintAmounts(t *testing.T) {
	st := newTestStore(t)
	p, _ := st.CreatePool("TACm", "TIUSD", 30, 1.0) // S=1, price=1

	// 寬區間涵蓋當前價格：[-600, 600]，各投入 100。
	r, err := st.MintPosition("0xowner1", p.Id, -600, 600, 100, 100)
	if err != nil {
		t.Fatalf("MintPosition: %v", err)
	}
	if r.PositionId <= 0 || r.Liquidity <= 0 {
		t.Fatalf("回傳異常: %+v", r)
	}
	// 在 S=1 對稱區間，實際投入應接近 100/100。
	if !almostEq(r.Amount0, 100, 1) || !almostEq(r.Amount1, 100, 1) {
		t.Fatalf("對稱區間實際投入應≈100/100, got %.4f/%.4f", r.Amount0, r.Amount1)
	}

	// 區間在當前價格上方（S=1 < sqrtA，此位置屆時全數為 token0）：建立時只需 token0。
	r2, err := st.MintPosition("0xowner2", p.Id, 600, 1200, 100, 0)
	if err != nil {
		t.Fatalf("上方區間 mint: %v", err)
	}
	if r2.Amount1 > 1e-6 {
		t.Fatalf("區間在價格上方的位置建立時應只需 token0, amount1=%.6f", r2.Amount1)
	}
	if r2.Amount0 <= 0 {
		t.Fatal("應有 token0 投入")
	}

	// 邊界錯誤。
	if _, err := st.MintPosition("0xowner3", p.Id, 600, 600, 1, 1); err == nil {
		t.Fatal("tickLower>=tickUpper 應失敗")
	}
}

func TestSwapBothDirections(t *testing.T) {
	st := newTestStore(t)
	p, _ := st.CreatePool("TACm", "TIUSD", 30, 1.0)
	_, _ = st.MintPosition("0xliq", p.Id, -600, 600, 1000, 1000)

	before, _ := st.GetPool(p.Id)

	// zeroForOne：賣 TACm(token0) 買 TIUSD(token1) → 價格下跌、tick 變小。
	swap, err := st.Swap(p.Id, true, 10)
	if err != nil {
		t.Fatalf("swap zeroForOne: %v", err)
	}
	if swap.AmountOut <= 0 {
		t.Fatal("輸出量應 > 0")
	}
	if swap.Tick >= before.Tick {
		t.Fatalf("zeroForOne 應使 tick 下跌: before=%d after=%d", before.Tick, swap.Tick)
	}
	if swap.SqrtPrice >= before.SqrtPrice {
		t.Fatal("zeroForOne 應使 sqrtPrice 下跌")
	}
	// 手續費 ≈ 10 × 0.003 = 0.03（毛額，分段可能略異）。
	if swap.Fee <= 0 {
		t.Fatal("應有手續費")
	}
	t.Logf("zeroForOne: in=%.4f out=%.4f fee=%.5f tick=%d sqrt=%.6f",
		swap.AmountIn, swap.AmountOut, swap.Fee, swap.Tick, swap.SqrtPrice)

	mid, _ := st.GetPool(p.Id)

	// oneForZero：反向 → 價格回升。
	swap2, err := st.Swap(p.Id, false, 10)
	if err != nil {
		t.Fatalf("swap oneForZero: %v", err)
	}
	if swap2.AmountOut <= 0 {
		t.Fatal("反向輸出量應 > 0")
	}
	after, _ := st.GetPool(p.Id)
	if after.Tick <= mid.Tick {
		t.Fatalf("oneForZero 應使 tick 回升: mid=%d after=%d", mid.Tick, after.Tick)
	}
	t.Logf("oneForZero: in=%.4f out=%.4f fee=%.5f tick=%d sqrt=%.6f",
		swap2.AmountIn, swap2.AmountOut, swap2.Fee, swap2.Tick, after.SqrtPrice)
}

func TestSwapCrossTick(t *testing.T) {
	st := newTestStore(t)
	p, _ := st.CreatePool("TACm", "TIUSD", 30, 1.0) // tick=0
	// 位置 A：寬幅涵蓋 0，提供活躍流動性。
	a, _ := st.MintPosition("0xa", p.Id, -600, 600, 500, 500)
	liqBefore, _ := st.GetPool(p.Id)

	// 位置 B：只在價格上方 [600, 1200] —— 當前價格在區間下方，需提供 token0。
	// 尚未活躍；但 tick=600 已註冊 liquidityNet=+LB（B 的下界）。
	b, err := st.MintPosition("0xb", p.Id, 600, 1200, 500, 0)
	if err != nil {
		t.Fatalf("mint B: %v", err)
	}
	// 以剛好足以穿越 tick=600、但不耗盡 B 區間的量做 oneForZero（賣 token1 買 token0，價格上漲）。
	// 穿越後：A 在 600 上界關閉（−LA）、B 在 600 下界啟用（+LB），活躍 L 應由 LA 切換為 LB。
	swap, err := st.Swap(p.Id, false, 600)
	if err != nil {
		t.Fatalf("swap: %v", err)
	}
	after, _ := st.GetPool(p.Id)
	t.Logf("cross tick: tick %d→%d, L %.2f→%.2f (A.L=%.2f B.L=%.2f), out=%.4f",
		liqBefore.Tick, after.Tick, liqBefore.Liquidity, after.Liquidity, a.Liquidity, b.Liquidity, swap.AmountOut)
	if after.Tick < 600 || after.Tick >= 1200 {
		t.Fatalf("應穿越 tick=600 並停在 B 區間內, got tick=%d", after.Tick)
	}
	if swap.AmountOut <= 0 {
		t.Fatal("輸出量應 > 0")
	}
	// 穿越後活躍 L 應接近 B 的流動性（≈LB），而非停留在 A 的流動性。
	if !almostEq(after.Liquidity, b.Liquidity, b.Liquidity*0.05) {
		t.Fatalf("穿越後活躍 L 應≈B.L=%.2f, got %.2f", b.Liquidity, after.Liquidity)
	}
	if almostEq(after.Liquidity, a.Liquidity, 1.0) {
		t.Fatal("穿越後活躍 L 不應仍等於 A.L（證明未切換）")
	}
}

func TestDecreaseAndCollect(t *testing.T) {
	st := newTestStore(t)
	p, _ := st.CreatePool("TACm", "TIUSD", 30, 1.0)
	m, _ := st.MintPosition("0xliq", p.Id, -600, 600, 1000, 1000)

	// 跑幾筆 swap 產生費用。
	if _, err := st.Swap(p.Id, true, 50); err != nil {
		t.Fatalf("swap: %v", err)
	}
	if _, err := st.Swap(p.Id, false, 50); err != nil {
		t.Fatalf("swap: %v", err)
	}

	// collect：應收到至少一些費用（兩個方向的費用落在 token0/token1）。
	cr, err := st.CollectFees(m.PositionId)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	t.Logf("collect: owed0=%.6f owed1=%.6f", cr.Amount0, cr.Amount1)
	if cr.Amount0 < 0 || cr.Amount1 < 0 {
		t.Fatal("費用不可為負")
	}
	// 再次 collect 應歸零（已領走）。
	cr2, _ := st.CollectFees(m.PositionId)
	if cr2.Amount0 > 1e-9 || cr2.Amount1 > 1e-9 {
		t.Fatalf("重複 collect 應為 0, got %.6f/%.6f", cr2.Amount0, cr2.Amount1)
	}

	// decrease 一半流動性。
	pos, _ := st.GetPosition(m.PositionId)
	dec, err := st.DecreasePosition(m.PositionId, pos.Liquidity/2)
	if err != nil {
		t.Fatalf("decrease: %v", err)
	}
	if dec.Amount0 < 0 || dec.Amount1 < 0 {
		t.Fatal("撤流金額不可為負")
	}
	if !almostEq(dec.LiquidityRemaining, pos.Liquidity/2, 1e-6) {
		t.Fatalf("剩餘 L 應≈一半, got %.4f want %.4f", dec.LiquidityRemaining, pos.Liquidity/2)
	}
	// 全撤。
	decAll, _ := st.DecreasePosition(m.PositionId, dec.LiquidityRemaining)
	if decAll.Amount0 < 0 || decAll.Amount1 < 0 {
		t.Fatal("全撤金額不可為負")
	}
	if decAll.LiquidityRemaining != 0 {
		t.Fatalf("全撤後 L 應=0, got %.4f", decAll.LiquidityRemaining)
	}
	t.Logf("decrease half: %.4f/%.4f; all: %.4f/%.4f", dec.Amount0, dec.Amount1, decAll.Amount0, decAll.Amount1)
}

func TestIncreasePosition(t *testing.T) {
	st := newTestStore(t)
	p, _ := st.CreatePool("TACm", "TIUSD", 30, 1.0)
	m, _ := st.MintPosition("0xliq", p.Id, -600, 600, 100, 100)
	inc, err := st.IncreasePosition(m.PositionId, 50, 50)
	if err != nil {
		t.Fatalf("increase: %v", err)
	}
	if inc.Liquidity <= m.Liquidity {
		t.Fatalf("increase 後 L 應增加: before=%.2f after=%.2f", m.Liquidity, inc.Liquidity)
	}
	if inc.Amount0 <= 0 || inc.Amount1 <= 0 {
		t.Fatal("increase 應有實際投入")
	}
}

func TestListPositions(t *testing.T) {
	st := newTestStore(t)
	p, _ := st.CreatePool("TACm", "TIUSD", 30, 1.0)
	_, _ = st.MintPosition("0xalice", p.Id, -600, 600, 100, 100)
	_, _ = st.MintPosition("0xalice", p.Id, 600, 1200, 100, 0)
	_, _ = st.MintPosition("0xbob", p.Id, -600, 0, 100, 0)

	list, err := st.ListPositions("0xalice")
	if err != nil {
		t.Fatalf("ListPositions: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("alice 應有 2 個位置, got %d", len(list))
	}
	if _, err := st.GetPosition(list[0].Id); err != nil {
		t.Fatalf("GetPosition: %v", err)
	}
}
