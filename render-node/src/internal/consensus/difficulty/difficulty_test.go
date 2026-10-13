package difficulty

import "testing"

func lookupFromMap(m map[int64]float64) BlockLookup {
	return func(h int64) (float64, bool) {
		v, ok := m[h]
		return v, ok
	}
}

func TestComputeRetarget(t *testing.T) {
	// [A] 基本方向
	tsNormal := make([]float64, RetargetInterval+1)
	for i := range tsNormal {
		tsNormal[i] = float64(i) // span=10, target=10
	}
	if got := ComputeRetarget(4, tsNormal, 1.0); got != 4 {
		t.Errorf("正常節奏難度不變, got=%d", got)
	}

	tsSlow := make([]float64, RetargetInterval+1)
	for i := range tsSlow {
		tsSlow[i] = float64(i) * 4.0 // span=40 → /4
	}
	if got := ComputeRetarget(4, tsSlow, 1.0); got != 1 {
		t.Errorf("太慢應降為1, got=%d", got)
	}

	tsFast := make([]float64, RetargetInterval+1)
	for i := range tsFast {
		tsFast[i] = float64(i) * 0.25 // span=2.5 → ×4
	}
	if got := ComputeRetarget(2, tsFast, 1.0); got != 8 {
		t.Errorf("太快應升為8, got=%d", got)
	}

	// [B] Clamp 與上下限
	tsHuge := make([]float64, RetargetInterval+1)
	tsHuge[0] = 0.0
	for i := 1; i <= RetargetInterval; i++ {
		tsHuge[i] = 1_000_000.0
	}
	if got := ComputeRetarget(8, tsHuge, 1.0); got != 2 {
		t.Errorf("極慢最多降至2, got=%d", got)
	}

	tsZero := make([]float64, RetargetInterval+1)
	if got := ComputeRetarget(4, tsZero, 1.0); got != MaxDifficulty {
		t.Errorf("極快應觸上限8, got=%d", got)
	}
	if got := ComputeRetarget(8, tsFast, 1.0); got > MaxDifficulty {
		t.Errorf("不得超上限, got=%d", got)
	}
	if got := ComputeRetarget(1, tsHuge, 1.0); got < MinDifficulty {
		t.Errorf("不得低於下限, got=%d", got)
	}
}

func TestShouldRetarget(t *testing.T) {
	cases := []struct {
		h    int64
		want bool
	}{
		{0, false}, {10, true}, {20, true}, {15, false},
	}
	for _, c := range cases {
		if got := ShouldRetarget(c.h, RetargetInterval); got != c.want {
			t.Errorf("ShouldRetarget(%d)=%v want %v", c.h, got, c.want)
		}
	}
}

func TestWindowSampling(t *testing.T) {
	blocks := map[int64]float64{}
	for h := int64(0); h <= int64(RetargetInterval+4); h++ {
		blocks[h] = float64(h) * 2
	}
	w := WindowTimestampsForHeight(lookupFromMap(blocks), int64(RetargetInterval), RetargetInterval)
	if len(w) != RetargetInterval+1 {
		t.Fatalf("窗口應有 %d 點, got=%d", RetargetInterval+1, len(w))
	}
	if w[0] != 0 || w[RetargetInterval] != float64(RetargetInterval)*2 {
		t.Errorf("窗口時間戳錯誤: w[0]=%v w[last]=%v", w[0], w[RetargetInterval])
	}
}

func TestReplay(t *testing.T) {
	// 恆慢鏈 h: h*2
	slow := map[int64]float64{}
	for h := int64(0); h <= int64(3*RetargetInterval); h++ {
		slow[h] = float64(h) * 2
	}
	step := 8
	for _, hh := range []int64{int64(RetargetInterval), 2 * int64(RetargetInterval), 3 * int64(RetargetInterval)} {
		ww := WindowTimestampsForHeight(lookupFromMap(slow), hh, RetargetInterval)
		step = ComputeRetarget(step, ww, 1.0)
	}
	got := ReplayDifficulty(lookupFromMap(slow), 3*int64(RetargetInterval), 8, 1.0, RetargetInterval)
	if got != step {
		t.Errorf("重放應與逐步一致: replay=%d step=%d", got, step)
	}
	if step >= 8 {
		t.Errorf("持續偏慢應下降, step=%d", step)
	}

	// 恆快鏈 h: h*0.1 → 觸上限
	fast := map[int64]float64{}
	for h := int64(0); h <= int64(3*RetargetInterval); h++ {
		fast[h] = float64(h) * 0.1
	}
	if got := ReplayDifficulty(lookupFromMap(fast), 3*int64(RetargetInterval), 2, 1.0, RetargetInterval); got != MaxDifficulty {
		t.Errorf("持續偏快應觸上限, got=%d", got)
	}

	// 空鏈/高度不足 → 維持基礎
	if got := ReplayDifficulty(func(int64) (float64, bool) { return 0, false }, 5, 3, 1.0, RetargetInterval); got != 3 {
		t.Errorf("高度不足應維持3, got=%d", got)
	}
}
