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

	// 預設費率（整幣 TACm）。低額預設，考量未來 TACm 價格可能上升；
	// 實際運行費率由鏈上治理參數 ai_fee_turn/ai_fee_file 熱更新（見 node.go）。
	// 豆包式低額預設：基礎費率調低 10 倍，再乘模型分級倍數（1×/3×/10×）。
	DefaultAIFeeTurn = 0.0001
	DefaultAIFeeFile = 0.0005
)

// titanBilling 是回傳給前端的計費明細。
type titanBilling struct {
	Charged    bool    `json:"charged"`
	Asset      string  `json:"asset"`
	Amount     float64 `json:"amount"`
	FeeAccount string  `json:"fee_account"`
	Reason     string  `json:"reason,omitempty"`
}

// currentAgentFees 讀取當前（鏈上治理後的）費率。
func (n *Node) currentAgentFees() (turn, file float64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.aiFeeTurn, n.aiFeeFile
}

// modelFeeMultiplier 按推理難度（模型強度）分級收費，基數為鏈上治理的基礎費率。
// rules（最弱）1×、llama（中）3×、openai（最強）10×；未知型別一律基礎 1×。
func modelFeeMultiplier(model string) float64 {
	switch model {
	case "llama":
		return 3.0
	case "openai":
		return 10.0
	default:
		return 1.0
	}
}

// meterAgentTurn 依本輪是否生成檔案與所用模型強度決定費率，並向連結地址扣費。
// 回傳明細；不向上拋錯——addr 為空或餘額不足時 graceful skip（Charged=false），
// 已連結且餘額足夠時真實扣費（Charged=true）。基礎費率取鏈上治理值，再乘模型分級倍數。
func (n *Node) meterAgentTurn(addr string, fileGenerated bool, model string) (titanBilling, error) {
	turnFee, fileFee := n.currentAgentFees()
	base := turnFee
	reason := "assistant_turn"
	if fileGenerated {
		base = fileFee
		reason = "file_generation"
	}
	rate := base * modelFeeMultiplier(model)
	b := titanBilling{Asset: string(wallet.AssetTACm), Amount: rate, FeeAccount: titanFeeAccount}
	if addr == "" {
		b.Reason = "no_linked_address"
		return b, nil
	}
	amt := wallet.NewAmountF(rate, wallet.AssetTACm)
	if amt.IsZero() {
		// 治理可將基礎費率設為 0（免費）：不扣費，正常提供服務。
		b.Reason = "zero_fee"
		return b, nil
	}
	if _, err := n.walletSvc.Transfer(addr, titanFeeAccount, wallet.AssetTACm, amt, "titan:ai:"+reason+":"+model); err != nil {
		// 餘額不足等：不阻斷服務，標記未扣費，由前端提示充值。
		b.Charged = false
		b.Reason = "insufficient_balance"
		return b, nil
	}
	b.Charged = true
	b.Reason = reason
	return b, nil
}
