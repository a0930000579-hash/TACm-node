package aimarket

// billing.go — AI 模型市場計費拆分（純狀態側）。
// 資金實際移轉在 node handler：ownerPart 由 caller → owner，
// protocolFee 由 caller → FeeAccount("aimarket_fee")。本檔只做金額拆分與常數。

import "math"

// Split 把一筆價格拆成 (ownerPart, protocolFee)。
// ownerPart = price × (1 - ProtocolFeeRate)；protocolFee = price × ProtocolFeeRate。
// 金額四捨五入至 4 位小數，避免浮點尾差。
func Split(price float64) (ownerPart, protocolFee float64) {
	if price <= 0 {
		return 0, 0
	}
	protocolFee = round4(price * ProtocolFeeRate)
	ownerPart = round4(price - protocolFee)
	return ownerPart, protocolFee
}

// round4 四捨五入至 4 位小數。
func round4(v float64) float64 {
	return math.Round(v*10000) / 10000
}

// InvokeResult invoke 的回執（stub 結果；實際外部模型呼叫由 handler 可選轉發，
// 失敗即回落 stub）。
type InvokeResult struct {
	InvocationId int64   `json:"invocation_id"`
	ModelId      int64   `json:"model_id"`
	Version      string  `json:"version"`
	Caller       string  `json:"caller"`
	Output       string  `json:"output"`
	Source       string  `json:"source"` // "stub" | "relay"
	Charged      float64 `json:"charged"`
	OwnerPart    float64 `json:"owner_part"`
	ProtocolFee  float64 `json:"protocol_fee"`
	FeeAccount   string  `json:"fee_account"`
	CreatedAt    int64   `json:"created_at"`
}
