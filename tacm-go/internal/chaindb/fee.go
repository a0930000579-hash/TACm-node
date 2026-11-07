package chaindb

// EIP-1559 動態手續費模型（M74-3）——與以太坊/BSC 的 gas 市場機制對齊：
//   - 每塊 header 記錄 base_fee（每 gas 的基礎價格），由前塊 gas 使用率動態調整；
//   - 交易可選帶 max_fee（每 gas 願付上限）與 priority_fee（給出塊者的小費）；
//   - 有效 gas 價 = min(max_fee, base_fee + priority_fee)；
//   - 總費用 = gas_used × 有效價，其中 base_fee×gas_used 銷毀（通縮），
//     其餘（priority 部分）歸出塊者進入 coinbase 分潤；
//   - 未帶新欄位的 legacy 交易沿用固定 fee（全部歸出塊者），向後相容。

const (
	// InitialBaseFee 創世 base fee（TACm / gas）。1e-6 × 21000 gas ≈ 0.021 TACm/筆。
	InitialBaseFee = 0.000001
	// BlockGasLimit 每塊 gas 上限（與主流鏈同級：30M）。
	BlockGasLimit = 30000000
	// TxGasBase 每筆普通交易的基本 gas（EIP-1559 標準轉帳 21000）。
	TxGasBase = 21000
	// MinBaseFee 調整下限（避免 base fee 歸零）。
	MinBaseFee = 0.00000001
)

// EffectiveGasPrice 計算有效 gas 價：max_fee=0 視為不設上限。
func EffectiveGasPrice(baseFee, maxFee, priorityFee float64) float64 {
	floor := baseFee + priorityFee
	if maxFee > 0 && maxFee < floor {
		return maxFee
	}
	return floor
}

// ComputeNextBaseFee 依 EIP-1559 公式計算下一個區塊的 base fee：
// new = parent + parent×(used−target)/target/8；used≤target 時下降（最多 −12.5%），
// 超過 target 時按超額比例上升；一律不低於 MinBaseFee。
func ComputeNextBaseFee(parentBaseFee float64, parentGasUsed, parentGasLimit int64) float64 {
	if parentBaseFee <= 0 {
		parentBaseFee = InitialBaseFee
	}
	if parentGasLimit <= 0 {
		return parentBaseFee
	}
	target := parentGasLimit / 2
	delta := parentBaseFee * float64(parentGasUsed-target) / float64(target) / 8
	next := parentBaseFee + delta
	if next < MinBaseFee {
		next = MinBaseFee
	}
	return next
}

// SplitFee 將一筆交易的總手續費拆成兩部分：
//   - burn：base_fee × gas_used（銷毀，任何帳戶都不入賬，全鏈通縮）；
//   - tip：剩餘部分（priority 小費，歸出塊者進入 coinbase 分潤）。
func SplitFee(total, baseFee float64, gasUsed int64) (burn, tip float64) {
	b := baseFee * float64(gasUsed)
	if b > total {
		b = total
	}
	return b, total - b
}

// TxGasLimit 傳回交易的 gas 上限（缺省 21000）。
func TxGasLimit(gasLimit int64) int64 {
	if gasLimit <= 0 {
		return TxGasBase
	}
	return gasLimit
}
