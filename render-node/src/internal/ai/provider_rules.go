package ai

import (
	"context"
	"fmt"
	"strings"
)

// RulesProvider 是離線/規則式 Provider：免金鑰、免模型即可完整示範 Agent 迴圈。
//
// v165 升級重點（對應「通用、人性化 AI 助手」）：
//   - 鏈上意圖（餘額/行情/交易/池/寫操作）→ 選工具（保留既有關鍵字路由與 8 工具）；
//   - 非鏈問題（日常、規劃、寫作、解釋、翻譯、學習、問候）→ 直接自然語言回答，
//     「不」呼叫工具；回應依子意圖與語言變化，並帶一句記憶接續，非固定模板；
//   - 多輪上下文：短句/指代續問（「那它今天呢？」）自動接回上一輪鏈上主題。
//
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
	// 找出最後一則 user 訊息、最後一輪 tool 結果，以及完整歷史（上下文串接用）。
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

	// 若已有工具結果 → 綜合成最終回答（迴圈收斂）；語言跟隨本輪使用者訊息。
	if len(toolResults) > 0 {
		return Turn{Answer: p.synthesize(toolResults, detectLang(lastUser)), Thought: "Synthesize tool results into a final answer."}, nil
	}

	turn := Turn{Thought: "Decide whether this needs an on-chain tool or a direct human reply."}
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

	// 上下文串接：短句/指代續問 → 接回上一輪鏈上主題（餘額／行情／交易）。
	if topic := lastChainTopic(msgs); topic != "" && looksLikeFollowUp(lastUser) {
		switch topic {
		case topicPrice:
			turn.ToolCalls = append(turn.ToolCalls, ToolCall{ID: "c-fprice", Name: "get_price", Args: map[string]any{"symbol": extractSymbol(lastUser)}})
			return turn, nil
		case topicBalance:
			turn.ToolCalls = append(turn.ToolCalls, ToolCall{ID: "c-fbal", Name: "get_balance", Args: map[string]any{"address": addr}})
			return turn, nil
		case topicTxs:
			turn.ToolCalls = append(turn.ToolCalls, ToolCall{ID: "c-ftxs", Name: "get_recent_txs", Args: map[string]any{"address": addr}})
			return turn, nil
		}
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
	case has("新聞", "新闻", "news", "時事", "时事", "天氣", "天气", "weather", "突發", "突发",
		"headline", "股市", "大盤", "大盘", "美股", "港股", "國際油價", "global market", "stock market", "current events"):
		// 需要最新資訊/時事/總體行情 → 聯網搜尋（不影響鏈上工具）。
		turn.ToolCalls = append(turn.ToolCalls, ToolCall{ID: "c-web", Name: "web_search", Args: map[string]any{"query": lastUser, "lang": detectLang(lastUser)}})
	default:
		// 非鏈問題（日常／規劃／寫作／解釋／翻譯／學習／問候）→ 人性化直接回答，不呼叫工具。
		return Turn{Answer: p.generalReply(lastUser, detectLang(lastUser), msgs), Thought: "Non-chain request: answer directly without tools."}, nil
	}
	return turn, nil
}

// synthesize 把工具結果綜合成自然語言回答；lang 跟隨本輪使用者語言（zh/en）。
// 鏈上欄位標籤維持既有英文（與現有測試一致）；聯網欄位標籤本地化。
func (p *RulesProvider) synthesize(results []ChatMessage, lang string) string {
	var b strings.Builder
	webLbl, fetchLbl := "Web search results: ", "Fetched page content: "
	if lang == "zh" {
		webLbl, fetchLbl = "網路搜尋結果：", "擷取的網頁內容："
	}
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
		case "web_search":
			fmt.Fprintf(&b, "%s%s\n", webLbl, r.Content)
		case "web_fetch":
			fmt.Fprintf(&b, "%s%s\n", fetchLbl, r.Content)
		default:
			fmt.Fprintf(&b, "%s: %s\n", r.Name, r.Content)
		}
	}
	b.WriteString("\nRisk note: this is on-chain data; DeFi actions carry market and smart-contract risk. Not investment advice.")
	return b.String()
}

// generalReply 離線/免金鑰模式下的「人性化」通用回答。
// 依子意圖（greeting/thanks/help/translate/writing/planning/explain/learn/who/general）
// 與語言組裝自然回覆，並在有上文鏈上主題時帶一句記憶接續——非單一固定模板。
func (p *RulesProvider) generalReply(text, lang string, history []ChatMessage) string {
	zh := lang == "zh"
	sub := detectSubIntent(text)

	// 記憶接續：若上一輪聊過鏈上主題，順手一問。
	nod := ""
	switch lastChainTopic(history) {
	case topicPrice:
		if zh {
			nod = "\n（對了，我們剛剛聊到行情——需要我再幫你查一次嗎？）"
		} else {
			nod = "\n(By the way, we just looked at prices — want me to check again?)"
		}
	case topicBalance:
		if zh {
			nod = "\n（對了，剛才查過你的餘額——要我再確認最新狀態嗎？）"
		} else {
			nod = "\n(By the way, we checked your balance a moment ago — want a refresh?)"
		}
	case topicTxs:
		if zh {
			nod = "\n（對了，剛才看過你的近期交易——還有其他想查的嗎？）"
		} else {
			nod = "\n(By the way, we reviewed your recent transactions — anything else?)"
		}
	}

	var body string
	switch sub {
	case "greeting":
		if zh {
			body = "你好！我是 Titan Ai，除了 TACm 鏈上數據，也能陪你規劃、寫作、解釋觀念或翻譯。今天想聊點什麼？"
		} else {
			body = "Hi! I'm Titan Ai. Beyond TACm on-chain data, I can help you plan, write, explain concepts or translate. What would you like to work on today?"
		}
	case "thanks":
		if zh {
			body = "不客氣！有需要隨時叫我——無論是鏈上查詢還是日常問題都可以。"
		} else {
			body = "You're welcome! Reach out anytime — whether it's an on-chain query or an everyday question."
		}
	case "bye":
		if zh {
			body = "再見！需要時再回來找我，我都在。"
		} else {
			body = "Goodbye! Come back anytime — I'll be here."
		}
	case "help":
		if zh {
			body = "我能幫你做兩類事：\n1) 通用對話：規劃、寫作、解釋、翻譯、學習，直接問我就好；\n2) 鏈上服務：查詢餘額、行情、近期交易、流動性池，或在你確認後準備 DEX 兌換／DeFi 存款借款（節點不會代你簽名）。\n想先試哪一個？"
		} else {
			body = "I can help in two ways:\n1) General chat: planning, writing, explaining, translating, learning — just ask.\n2) On-chain: check balance, prices, recent transactions and pools, or prepare DEX swaps / DeFi deposits & borrows after your confirmation (the node never signs for you).\nWhich would you like to try?"
		}
	case "translate":
		if zh {
			body = "翻譯我可以幫忙——請把要翻的句子貼給我，並說目標語言（例如「把這句翻成英文」）。我會給自然的譯文，也可順手潤稿。"
		} else {
			body = "Happy to help with translation — paste the text and tell me the target language (e.g. 'translate this to English'). I'll give a natural rendering and can polish it too."
		}
	case "writing":
		if zh {
			body = "寫作我可以一起來：給我主題、目的與讀者，我先擬一版大綱或草稿，你再告訴我要調整的語氣（正式／親切／精簡）。"
		} else {
			body = "Let's write together: give me the topic, purpose and audience, and I'll draft an outline or a first version — then tell me the tone (formal / friendly / concise)."
		}
	case "planning":
		if zh {
			body = "規劃的話，先告訴我目標與時間範圍，我幫你拆成可行的步驟與檢查點；若會牽涉鏈上持倉，我可以一併提示風險（但不構成投資建議）。"
		} else {
			body = "For planning, tell me your goal and timeframe and I'll break it into steps and checkpoints. If it touches on-chain holdings I'll flag the risk — without giving investment advice."
		}
	case "explain":
		if zh {
			body = "你想了解什麼？給我一個名詞或現象（例如 PoS、區塊確定性、DeFi 流動性池），我先用白話比喻講清楚，再問你想深入哪一段。"
		} else {
			body = "What would you like explained? Give me a term or idea (e.g. PoS, block finality, a DeFi pool) and I'll explain it in plain language, then ask which part to go deeper on."
		}
	case "learn":
		if zh {
			body = "學習建議用「概念 → 範例 → 小練習」的方式。告訴我你想學的主題與目前程度，我從最適合的起點帶你。"
		} else {
			body = "To learn something, I'd suggest concept → example → a tiny exercise. Tell me the topic and your current level, and I'll start at the right spot."
		}
	case "coding":
		// 程式問題：以既有知識給實質骨架＋簡潔說明，並誠實標示離線規則模式。
		low := strings.ToLower(text)
		if zh {
			if strings.Contains(low, "sql") {
				body = "常見錯法 `SELECT FROM users` 缺少要查的欄位。正寫法：\n" +
					"```sql\nSELECT id, name, email FROM users WHERE active = 1 ORDER BY id LIMIT 20;\n```\n" +
					"若你只想取全部欄位，用 `SELECT * FROM users;` 也必須明確指定 `FROM`。完整報錯訊息貼給我可進一步定位。"
			} else {
				body = "我目前在離線規則模式（以規則模板回答，非完整 LLM），先給你一個可用的 Go goroutine worker pool 骨架：\n" +
					"```go\nfunc worker(ctx context.Context, in <-chan int) {\n\tfor {\n\t\tselect {\n\t\tcase <-ctx.Done():\n\t\t\treturn\n\t\tcase job := <-in:\n\t\t\t_ = process(job) // 你的業務處理\n\t\t}\n\t}\n}\n\nfunc main() {\n\tctx, cancel := context.WithCancel(context.Background())\n\tdefer cancel()\n\tin := make(chan int, 16)\n\tfor i := 0; i < 4; i++ { go worker(ctx, in) }\n\tfor j := 0; j < 100; j++ { in <- j }\n\tclose(in)\n}\n```\n" +
					"要點：用 context 控制生命週期、带 buffer 的 channel 解耦生產與消費、N 個 worker 並行消費。"
			}
			body += "\n\n（離線規則模式：複雜專案級程式碼與即時資訊建議接 llama.cpp 本地模型，或開啟 TAC_SEARCH_URL 聯網，我會誠實標示不裝作完整模型。）"
		} else {
			if strings.Contains(low, "sql") {
				body = "The common mistake `SELECT FROM users` is missing the column list. Correct form:\n" +
					"```sql\nSELECT id, name, email FROM users WHERE active = 1 ORDER BY id LIMIT 20;\n```\n" +
					"If you want all columns, use `SELECT * FROM users;` — you must still specify `FROM`. Paste the full error for a precise diagnosis."
			} else {
				body = "I'm currently in offline rule mode (a template-based reply, not a full LLM). Here's a usable Go goroutine worker pool skeleton:\n" +
					"```go\nfunc worker(ctx context.Context, in <-chan int) {\n\tfor {\n\t\tselect {\n\t\tcase <-ctx.Done():\n\t\t\treturn\n\t\tcase job := <-in:\n\t\t\t_ = process(job) // your work\n\t\t}\n\t}\n}\n\nfunc main() {\n\tctx, cancel := context.WithCancel(context.Background())\n\tdefer cancel()\n\tin := make(chan int, 16)\n\tfor i := 0; i < 4; i++ { go worker(ctx, in) }\n\tfor j := 0; j < 100; j++ { in <- j }\n\tclose(in)\n}\n```\n" +
					"Key points: context controls the lifecycle, a buffered channel decouples producer/consumer, and N workers consume in parallel."
			}
			body += "\n\n(Offline rule mode: for complex project-level code or live info, connect a local llama.cpp model or set TAC_SEARCH_URL — I stay honest that I'm not a full model here.)"
		}
	case "who":
		if zh {
			body = "我是 Titan Ai，TAC AI 智能鏈上的 AI 助手。我能陪你對話、規劃、寫作，也能在你確認後協助鏈上查詢與 DeFi 操作。"
		} else {
			body = "I'm Titan Ai, the AI assistant on the TAC AI Smart Chain. I can chat, plan and write, and help with on-chain queries and DeFi actions after your confirmation."
		}
	default:
		if zh {
			body = "這部分我可以陪你聊。以一般常識給你方向：例如睡眠不好，可先固定就寢時間、睡前一小時遠離螢幕、避免咖啡因與重訓，白天多接觸自然光；若長期影響作息，建議諮詢醫療專業。\n你也可以換個更具體的問法，例如「解釋 PoS」「幫我寫一封週報」「用 Python 寫個讀 CSV 的小程式」；需要鏈上數據時直接說餘額、行情或交易。" +
				"\n\n（目前為離線規則模式：需要更深入的即時資訊或複雜內容，可接 llama.cpp 本地模型或開啟 TAC_SEARCH_URL 聯網。）"
		} else {
			body = "I'm happy to talk this through. As general guidance — e.g. if sleep is off, try a consistent bedtime, screens off an hour before bed, cut caffeine and late exercise, and get daylight during the day; if it persists, see a health professional.\n" +
				"You can also be more specific: 'explain PoS', 'draft a weekly report', or 'write a small Python snippet to read a CSV'. For on-chain data, just ask about balance, prices or transactions." +
				"\n\n(Currently in offline rule mode: for deeper or live info, connect a local llama.cpp model or set TAC_SEARCH_URL.)"
		}
	}
	return body + nod
}

// detectSubIntent 把非鏈問題粗分為子意圖，用於人性化回覆。程式問題優先判定。
func detectSubIntent(text string) string {
	if looksLikeCode(text) {
		return "coding"
	}
	low := strings.ToLower(text)
	switch {
	case containsAny(low, []string{"你好", "您好", "哈囉", "哈喽", "嗨", "早安", "晚安", "早上好", "下午好", "hello", "hi ", "hey", "good morning", "good evening"}):
		return "greeting"
	case containsAny(low, []string{"謝謝", "谢谢", "感謝", "感谢", "thanks", "thank you", "thx", "appreciated"}):
		return "thanks"
	case containsAny(low, []string{"再見", "再见", "bye", "拜拜", "掰", "goodbye"}):
		return "bye"
	case containsAny(low, []string{"你能做什麼", "能做什麼", "你會什麼", "功能", "你會幫", "你可以幫我", "what can you do", "how can you help", "幫助", "说明", "說明", "help me"}):
		return "help"
	case containsAny(low, []string{"翻譯", "翻译", "translate", "翻成", "译成", "轉成英文", "转成英文"}):
		return "translate"
	case containsAny(low, []string{"寫", "写", "寫作", "写作", "改寫", "改写", "潤稿", "润稿", "作文", "寫一封", "寫個", "draft", "write", "essay", "email", "郵件", "邮件", "週報", "周报"}):
		return "writing"
	case containsAny(low, []string{"計畫", "计划", "規劃", "规划", "plan", "schedule", "安排", "目標", "目标", "goal", "待辦", "todo"}):
		return "planning"
	case containsAny(low, []string{"解釋", "解释", "什麼是", "什么是", "何謂", "為什麼", "为什么", "why", "explain", "what is", "怎麼運作", "怎么运作", "運作原理", "运作原理", "運作方式", "运作方式"}):
		return "explain"
	case containsAny(low, []string{"學習", "学习", "教我", "learn", "teach", "如何學", "怎么学", "怎麼學", "tutorial", "教學", "教学"}):
		return "learn"
	case containsAny(low, []string{"你是誰", "你是谁", "你叫什麼", "你叫什么", "who are you", "what are you"}):
		return "who"
	}
	return "general"
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
