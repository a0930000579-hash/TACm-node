package node

// agent_billing.go — TACm AI 用量計費（TACm）。
//
// 對齊 aimarket 計費模式：資金實際移轉走 walletSvc.Transfer（帳本授權，無需私鑰），
// 平台收入入 FeeAccount。本檔只定義費率、收費帳戶與計費函式，不新增業務邏輯。
//
// 分際：
//   - 節點不持有使用者私鑰、不代簽；僅在已連結地址且餘額足夠時扣費。
//   - 未連結地址（addr 為空）不扣費，回傳 skipped，避免阻斷公開展示。
//   - 餘額不足時回錯誤，由 handler 明確告知，不靜默放行高級動作。

import (
	"tacm/internal/wallet"
)

const (
	// titanFeeAccount TACm AI 計費收入帳戶。
	titanFeeAccount = "titan_ai_fee"

	// 費率（整幣 TACm，六位小數精度內）。
	// 一般對話/查詢每輪固定費用；檔案生成為高級動作，費用較高。
	titanFeeTurn = 0.01
	titanFeeFile = 0.05
)

// titanBilling 是回傳給前端的計費明細。
type titanBilling struct {
	Charged    bool    `json:"charged"`
	Asset      string  `json:"asset"`
	Amount     float64 `json:"amount"`
	FeeAccount string  `json:"fee_account"`
	Reason     string  `json:"reason,omitempty"`
}

// meterAgentTurn 依本輪是否生成檔案決定費率，並向連結地址扣費。
// 回傳明細；不向上拋錯——addr 為空或餘額不足時 graceful skip（Charged=false），
// 已連結且餘額足夠時真實扣費（Charged=true）。
func meterAgentTurn(svc *wallet.Service, addr string, fileGenerated bool) (titanBilling, error) {
	rate := titanFeeTurn
	reason := "assistant_turn"
	if fileGenerated {
		rate = titanFeeFile
		reason = "file_generation"
	}
	b := titanBilling{Asset: string(wallet.AssetTACm), Amount: rate, FeeAccount: titanFeeAccount}
	if addr == "" {
		b.Reason = "no_linked_address"
		return b, nil
	}
	amt := wallet.NewAmountF(rate, wallet.AssetTACm)
	if amt.IsZero() {
		b.Reason = "invalid_rate"
		return b, nil
	}
	if _, err := svc.Transfer(addr, titanFeeAccount, wallet.AssetTACm, amt, "titan:ai:"+reason); err != nil {
		// 餘額不足等：不阻斷服務，標記未扣費，由前端提示充值。
		b.Charged = false
		b.Reason = "insufficient_balance"
		return b, nil
	}
	b.Charged = true
	b.Reason = reason
	return b, nil
}
