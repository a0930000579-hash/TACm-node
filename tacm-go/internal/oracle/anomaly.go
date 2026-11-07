// Package oracle：偏離與異常檢測（anomaly detection）。
//
// 純函式：給一組 (source, price) 觀測值，算出
//   - 中位數（median，主聚合價）
//   - 加權平均（weighted mean，可選輔助）
//   - 離群標記（偏離中位數超過 band 的源剔除）
//   - 突變標記（新中位數相較上一輪跳動超過 jumpBand）
//
// 不持鎖、不讀外部狀態，便於單測。
package oracle

import (
	"math"
	"sort"
)

// obs 為單一源在某 symbol 上的一筆觀測。
type obs struct {
	Name  string
	Price float64
}

// aggStats 為一次聚合的中間結果。
type aggStats struct {
	Median   float64 // 最終採用中位數（剔除離群後）
	Weighted float64 // 加權平均（採用源）
	Adopted  []obs   // 採用（非離群）的觀測
	Outliers []obs   // 被剔除的離群觀測
}

// medianOf 回傳一組價格中位數（n 為偶數時取中間兩點均值）。
// 呼叫端需保證 len>0。
func medianOf(prices []float64) float64 {
	s := append([]float64(nil), prices...)
	sort.Float64s(s)
	n := len(s)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// classifyObservations：先對全部有效觀測取中位數，
// 再把偏離中位數超過 band（分率，如 0.10＝±10%）的源標 outlier 並剔除，
// 最後對採用集合重新取中位數與加權平均。
//
// band<=0 時預設 10%。回傳的 Adopted 可能為空（全部離群時由上層降級處理）。
func classifyObservations(all []obs, band float64, weights map[string]float64) aggStats {
	st := aggStats{}
	if len(all) == 0 {
		return st
	}
	if band <= 0 {
		band = 0.10
	}
	prices := make([]float64, 0, len(all))
	for _, o := range all {
		if !math.IsNaN(o.Price) && !math.IsInf(o.Price, 0) && o.Price > 0 {
			prices = append(prices, o.Price)
		}
	}
	if len(prices) == 0 {
		return st
	}
	base := medianOf(prices) // 初始參考中位數
	for _, o := range all {
		if math.IsNaN(o.Price) || math.IsInf(o.Price, 0) || o.Price <= 0 {
			continue
		}
		dev := math.Abs(o.Price-base) / base
		if dev > band {
			st.Outliers = append(st.Outliers, o)
			continue
		}
		st.Adopted = append(st.Adopted, o)
	}
	if len(st.Adopted) == 0 {
		// 全部偏離：保守起見退回全部觀測（不降為 0 價），由上層標 degraded。
		st.Adopted = append(st.Adopted, all...)
	}
	adoptedPxs := make([]float64, 0, len(st.Adopted))
	var ws, wp float64
	for _, o := range st.Adopted {
		adoptedPxs = append(adoptedPxs, o.Price)
		w := weights[o.Name]
		if w <= 0 {
			w = 1
		}
		ws += w
		wp += o.Price * w
	}
	st.Median = medianOf(adoptedPxs)
	if ws > 0 {
		st.Weighted = wp / ws
	} else {
		st.Weighted = st.Median
	}
	return st
}

// jumpDetected：新中位數相較上一有效中位數跳動是否超過 jumpBand（分率）。
// 無上一輪價（prev<=0）時回 false（首輪不標突變）。
func jumpDetected(prev, now, jumpBand float64) bool {
	if prev <= 0 || now <= 0 || jumpBand <= 0 {
		return false
	}
	return math.Abs(now-prev)/prev > jumpBand
}

// confidence：依採用源數占比與採用源價格離散度計算 0..1 置信度。
// adoptedN/totalN 提供基礎可信度；(1 - dispersion) 懲罰採用源彼此分歧。
func confidence(adoptedN, totalN int, adoptedPxs []float64) float64 {
	if totalN <= 0 {
		return 0
	}
	base := float64(adoptedN) / float64(totalN)
	if len(adoptedPxs) == 0 {
		return base * 0.5
	}
	min, max := adoptedPxs[0], adoptedPxs[0]
	for _, p := range adoptedPxs {
		if p < min {
			min = p
		}
		if p > max {
			max = p
		}
	}
	mid := medianOf(adoptedPxs)
	dispersion := 0.0
	if mid > 0 {
		dispersion = (max - min) / mid
	}
	tight := 1 - dispersion
	if tight < 0 {
		tight = 0
	}
	if tight > 1 {
		tight = 1
	}
	c := base * (0.5 + 0.5*tight)
	if c > 1 {
		c = 1
	}
	if c < 0 {
		c = 0
	}
	return c
}
