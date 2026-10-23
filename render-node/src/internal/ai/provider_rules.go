package ai

import (
	"context"
	"fmt"
	"strings"
)

// RulesProvider 是離線/規則式 Provider：免金鑰、免模型即可完整示範 Agent 迴圈。
// 意圖/關鍵字 → 選工具；工具結果回來後，由本 provider 綜合成最終回答。
// 這也示範了「工具失敗時換方法」：若上一輪工具回錯，下一輪改呼叫 read_memory 或直接回答。
type RulesProvider struct {
	// Address 預設查詢地址（無 address 參數時使用）。
	Address string
}

// NewRulesProvider 建立規則 provider。
func NewRulesProvider(address string) *RulesProvider {
	return &RulesProvider{Address: address}
}

// Name 回傳提供者名稱。
func (p *RulesProvider) Name() string { return "rules" }

// Complete 實作 Provider。
func (p *RulesProvider) Complete(ctx context.Context, msgs []ChatMessage, tools []*Tool) (Turn, error) {
	// 找出最後一則 user 訊息與最後一輪 tool 結果。
	var lastUser string
	var toolResults []ChatMessage
	for _, m := range msgs {
		switch m.Role {
		case "user":
			lastUser = m.Content
		case "tool":
			toolResults = append(toolResults, m)
		}
	}

	// 若已有工具結果 → 綜合成最終回答（迴圈收斂）。
	if len(toolResults) > 0 {
		return Turn{Answer: p.synthesize(toolResults), Thought: "Synthesize tool results into a final answer."}, nil
	}

	turn := Turn{Thought: "Decide which tool to call based on the request."}
	low := strings.ToLower(lastUser)
	has := func(keys ...string) bool {
		for _, k := range keys {
			if strings.Contains(low, k) {
				return true
			}
		}
		return false
	}

	addr := p.Address
	if a := extractAddress(lastUser); a != "" {
		addr = a
	}

	switch {
	case has("memory", "記憶", "记忆", "history", "對話紀錄", "对话记录"):
		turn.ToolCalls = append(turn.ToolCalls, ToolCall{ID: "c-mem", Name: "read_memory", Args: map[string]any{}})
	case has("price", "行情", "幣價", "币价", "股價", "股价", "ticker", "多少錢", "多少钱", "價格", "价格"):
		sym := extractSymbol(lastUser)
		turn.ToolCalls = append(turn.ToolCalls, ToolCall{ID: "c-price", Name: "get_price", Args: map[string]any{"symbol": sym}})
	case has("recent", "最近交易", "最近的交易", "历史交易", "歷史交易", "tx", "transactions", "轉帳紀錄", "转账记录"):
		turn.ToolCalls = append(turn.ToolCalls, ToolCall{ID: "c-txs", Name: "get_recent_txs", Args: map[string]any{"address": addr}})
	case has("swap", "兌換", "兑换", "換成", "换成", "exchange", "trade", "買", "卖", "賣", "buy", "sell"):
		turn.ToolCalls = append(turn.ToolCalls, ToolCall{ID: "c-swap", Name: "dex_swap", Args: map[string]any{"address": addr, "pair": "TAC/USDT", "amount": "0"}})
	case has("deposit", "存款", "存入", "stake", "質押", "质押", "lend"):
		turn.ToolCalls = append(turn.ToolCalls, ToolCall{ID: "c-dep", Name: "defi_deposit", Args: map[string]any{"address": addr, "asset": "TAC", "amount": "0"}})
	case has("borrow", "借", "借入"):
		turn.ToolCalls = append(turn.ToolCalls, ToolCall{ID: "c-bor", Name: "defi_borrow", Args: map[string]any{"address": addr, "asset": "USDT", "amount": "0"}})
	case has("balance", "餘額", "余额", "多少", "钱", "錢", "持有", "閒置", "闲置", "閒著", "闲著", "asset", "assets"):
		// 多步：餘額 + 近期交易（示範無依賴並行）。
		turn.ToolCalls = append(turn.ToolCalls,
			ToolCall{ID: "c-bal", Name: "get_balance", Args: map[string]any{"address": addr}},
			ToolCall{ID: "c-txs2", Name: "get_recent_txs", Args: map[string]any{"address": addr}},
		)
	default:
		// 未知意圖 → 直接回答能力介紹（不呼叫工具）。
		return Turn{Answer: p.help()}, nil
	}
	return turn, nil
}

// synthesize 把工具結果綜合成自然語言回答。
func (p *RulesProvider) synthesize(results []ChatMessage) string {
	var b strings.Builder
	for _, r := range results {
		switch r.Name {
		case "get_balance":
			fmt.Fprintf(&b, "On-chain balance: %s\n", r.Content)
		case "get_price":
			fmt.Fprintf(&b, "Market price: %s\n", r.Content)
		case "get_recent_txs":
			fmt.Fprintf(&b, "Recent transactions: %s\n", r.Content)
		case "read_memory":
			fmt.Fprintf(&b, "Conversation memory: %s\n", r.Content)
		default:
			fmt.Fprintf(&b, "%s: %s\n", r.Name, r.Content)
		}
	}
	b.WriteString("\nRisk note: this is on-chain data; DeFi actions carry market and smart-contract risk. Not investment advice.")
	return b.String()
}

func (p *RulesProvider) help() string {
	return "I'm the TACm AI Agent. I can: check your balance, look up prices, list recent transactions, " +
		"and prepare DEX swaps / DeFi deposits (with your confirmation). Try: 'what is my balance', " +
		"'price of TAC', or 'show my recent transactions'."
}

// extractAddress 從句子中抓 0x 開頭地址。
func extractAddress(s string) string {
	low := strings.ToLower(s)
	idx := strings.Index(low, "0x")
	if idx < 0 {
		return ""
	}
	rest := s[idx:]
	out := []rune{}
	for _, r := range rest {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') || r == 'x' {
			out = append(out, r)
		} else {
			break
		}
	}
	if len(out) >= 10 {
		return string(out)
	}
	return ""
}

// extractSymbol 從句子抓幣種符號（大寫字母）。
func extractSymbol(s string) string {
	for _, tok := range strings.Fields(s) {
		u := strings.ToUpper(strings.Trim(tok, ".,!?；;：:"))
		if len(u) >= 2 && len(u) <= 6 && isUpperAlpha(u) {
			return u + "/USDT"
		}
	}
	return "TAC/USDT"
}

func isUpperAlpha(s string) bool {
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}
