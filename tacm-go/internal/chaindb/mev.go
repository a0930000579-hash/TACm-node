package chaindb

import "strconv"

// 鏈原生 MEV / 優先費打包排序（EIP-1559 優先小費拍賣，非抄襲 Flashbots）：
//   - 有效優先小費（effective priority tip）是出塊者真正拿到的小費總額（TACm）；
//   - 打包策略可配置（預設按有效小費降序），所有比較器附確定性 tie-break，
//     保證所有誠實節點對同一 mempool 產生完全一致的打包順序（共識確定性，避免分叉）。
//
// 欄位單位（依 eth_rpc.go / fee.go 確認）：
//   - payload["fee"]          總手續費，TACm 十進位字串（= effectiveGasPrice × gas）；
//   - payload["max_fee"]      每 gas 願付上限，TACm/gas（僅 EIP-1559 type2 交易帶）；
//   - payload["priority_fee"] 每 gas 優先小費，TACm/gas（僅 type2 帶）；
//   - payload["gas_limit"]    gas 用量上限（單位：gas，int64）；
//   - baseFee                 本塊基礎價，TACm/gas（ComputeNextBaseFee 輸出的 float64）。

// PackStrategy 打包出隊策略（共識確定性：同一輸入所有節點結果一致）。
type PackStrategy int

const (
	// PackPriorityTip 預設：按有效優先小費降序（EIP-1559 tip 拍賣）。
	PackPriorityTip PackStrategy = iota
	// PackGasUsage：按 gas 用量上限降序，偏好高 gas 利用率的交易先出。
	PackGasUsage
	// PackFIFO：按入池時間先到先服務。
	PackFIFO
)

// String 便於日誌與設定映射。
func (s PackStrategy) String() string {
	switch s {
	case PackGasUsage:
		return "gas_usage"
	case PackFIFO:
		return "fifo"
	default:
		return "priority_tip"
	}
}

// ParsePackStrategy 由設定字串解析策略；未知值回預設 PackPriorityTip。
func ParsePackStrategy(s string) PackStrategy {
	switch s {
	case "gas_usage", "gas", "gasusage":
		return PackGasUsage
	case "fifo", "fifo_then_priority":
		return PackFIFO
	default:
		return PackPriorityTip
	}
}

// parseTacmFee 解析 TACm 十進位字串為 float64；缺失/負值/非法一律視為 0（不 panic）。
func parseTacmFee(s string) float64 {
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 {
		return 0
	}
	return f
}

// txGasUnits 解析交易 gas_limit；缺失/非正時回 TxGasBase（21000，與 TxGasLimit 一致）。
func txGasUnits(tx map[string]any) int64 {
	g := getInt64(tx, "gas_limit")
	if g <= 0 {
		return TxGasBase
	}
	return g
}

// EffectivePriorityTip 計算一筆交易的「有效優先小費」（可比較，單位 TACm 總額）。
//
//	EIP-1559（type2，帶 max_fee/priority_fee）：
//	  tipPerGas = min(priority_fee, max_fee - baseFee)
//	  有效總 tip = tipPerGas × gas_limit
//	  max_fee < baseFee 時有效價低於基礎價、無法被打包 → 回 0（排最後）。
//	legacy（無 max_fee/priority_fee）：全部手續費皆歸出塊者，有效總 tip = fee。
//
// 任何欄位缺失/非法值一律視為 0，決不 panic。
func EffectivePriorityTip(tx map[string]any, baseFee float64) float64 {
	maxFeeStr := getString(tx, "max_fee")
	prioStr := getString(tx, "priority_fee")
	has1559 := maxFeeStr != "" || prioStr != ""
	if !has1559 {
		// legacy：固定費全部是出塊者小費。
		return feeOf(tx)
	}
	mf := parseTacmFee(maxFeeStr)
	pf := parseTacmFee(prioStr)
	headroom := mf - baseFee
	if headroom <= 0 {
		return 0 // max_fee 不足以涵蓋本塊 baseFee → 不可打包
	}
	tipPerGas := pf
	if headroom < pf {
		tipPerGas = headroom
	}
	return tipPerGas * float64(txGasUnits(tx))
}

// pendingSortKey 為單筆交易在指定策略下的出隊排序鍵（primary 越大越優先）。
// 確定性 tie-break：primary 相同時先比 receivedAt（早者優先），再比 tx_hash 字典序。
type pendingSortKey struct {
	primary    float64
	receivedAt int64
	hash       string
}

// betterThan 回報 a 是否應排在 b 之前（嚴格、全序，無平手遺留）。
func (k pendingSortKey) betterThan(o pendingSortKey) bool {
	if k.primary != o.primary {
		return k.primary > o.primary
	}
	if k.receivedAt != o.receivedAt {
		return k.receivedAt < o.receivedAt
	}
	return k.hash < o.hash
}

// sortKeyFor 依策略計算排序鍵。
//   - PackPriorityTip：primary = 有效優先小費；
//   - PackGasUsage：primary = gas 用量上限；
//   - PackFIFO：primary = -receivedAt（越早入池 primary 越大）。
func sortKeyFor(tx map[string]any, receivedAt int64, baseFee float64, strategy PackStrategy) pendingSortKey {
	var primary float64
	switch strategy {
	case PackGasUsage:
		primary = float64(txGasUnits(tx))
	case PackFIFO:
		primary = -float64(receivedAt)
	default:
		primary = EffectivePriorityTip(tx, baseFee)
	}
	return pendingSortKey{
		primary:    primary,
		receivedAt: receivedAt,
		hash:       getString(tx, "tx_hash"),
	}
}
