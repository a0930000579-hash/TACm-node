// Package difficulty 實現 TAC Ai 智能鏈的 PoW 難度動態調整（按出塊時間目標，
// 類似比特幣 retarget，適配秒級快速出塊）。本包為純函數：輸入歷史時間戳即可
// 重放，不依賴外部狀態，可獨立測試，也可從創世重新計算當前難度。
package difficulty

import "math"

const (
	RetargetInterval   = 10  // 每 10 個區塊調整一次
	TargetBlockTime    = 1.0 // 目標出塊間隔（秒）
	MinDifficulty      = 1
	MaxDifficulty      = 8   // PoW 前導零上限
	MaxAdjustmentRatio = 4.0 // 單次最多 ×4 / ÷4
)

// BlockLookup 按高度返回該塊時間戳；塊不存在時 found=false。
type BlockLookup func(height int64) (ts float64, found bool)

// ComputeRetarget 根據一個調整窗口的時間戳計算新難度。
// windowTimestamps 按高度遞增、長度為區間數+1。
func ComputeRetarget(oldDifficulty int, windowTimestamps []float64, targetBlockTime float64) int {
	old := oldDifficulty
	if old < MinDifficulty {
		old = MinDifficulty
	}
	if len(windowTimestamps) < 2 || targetBlockTime <= 0 {
		return old
	}

	actualSpan := windowTimestamps[len(windowTimestamps)-1] - windowTimestamps[0]
	nIntervals := float64(len(windowTimestamps) - 1)
	targetSpan := nIntervals * targetBlockTime
	if targetSpan <= 0 {
		return old
	}
	if actualSpan <= 0 {
		actualSpan = 1e-6 // 防異常時間戳
	}

	ratio := actualSpan / targetSpan
	if ratio > MaxAdjustmentRatio {
		ratio = MaxAdjustmentRatio
	} else if ratio < 1.0/MaxAdjustmentRatio {
		ratio = 1.0 / MaxAdjustmentRatio
	}

	newF := float64(old) / ratio
	new := int(math.Round(newF))
	if new < MinDifficulty {
		return MinDifficulty
	}
	if new > MaxDifficulty {
		return MaxDifficulty
	}
	return new
}

// WindowTimestampsForHeight 挖出 completedHeight 後取窗口時間戳，覆蓋
// [completedHeight-interval, completedHeight]（含兩端），共 interval+1 個點。
func WindowTimestampsForHeight(lookup BlockLookup, completedHeight int64, interval int) []float64 {
	out := make([]float64, 0, interval+1)
	for h := completedHeight - int64(interval); h <= completedHeight; h++ {
		ts, found := lookup(h)
		if found {
			out = append(out, float64(ts))
		} else {
			out = append(out, 0.0)
		}
	}
	return out
}

// ShouldRetarget 判斷挖出 completedHeight 後是否到調整點。
func ShouldRetarget(completedHeight int64, interval int) bool {
	return completedHeight > 0 && interval > 0 && completedHeight%int64(interval) == 0
}

// ReplayDifficulty 從創世重放所有調整點，計算已挖到 tipHeight 時的動態基礎難度。
func ReplayDifficulty(lookup BlockLookup, tipHeight int64, baseDifficulty int,
	targetBlockTime float64, interval int) int {
	diff := baseDifficulty
	if diff < MinDifficulty {
		diff = MinDifficulty
	}
	if interval <= 0 {
		return diff
	}
	for h := int64(interval); h <= tipHeight; h += int64(interval) {
		ts := WindowTimestampsForHeight(lookup, h, interval)
		diff = ComputeRetarget(diff, ts, targetBlockTime)
	}
	return diff
}
