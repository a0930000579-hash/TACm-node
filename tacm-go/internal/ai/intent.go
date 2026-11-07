package ai

import (
	"strings"
	"unicode"
)

// v165：Titan Ai 從「鏈上問答」升級為通用、人性化 AI 助手。
//
// 本檔負責三件事（純函式、無狀態、可測試）：
//   - Intent：把使用者訊息分類為 閒聊(chat) / 查詢(query) / 操作(action)；
//   - detectLang：輕量語言偵測（含中日韓統一表意文字 → zh，否則 en），
//     讓離線/免金鑰 fallback 也能用使用者語言人性化回應，不需 HTTP 層傳 lang；
//   - 上下文串接：從對話記憶抓「最近鏈上主題」，讓「那它現在呢？」這類
//     指代/續問能接回上一輪主題（餘額／行情／交易）。
//
// 設計原則：關鍵字表與 provider_rules.go 的工具路由同源，避免「分類說查詢、
// 路由卻沒進工具」或相反的不一致。

// Intent 意圖分類。
type Intent string

const (
	// IntentChat 閒聊／一般對話：日常、規劃、寫作、解釋、翻譯、學習、問候等。
	// 此類「不」呼叫鏈上工具，直接自然語言回答。
	IntentChat Intent = "chat"
	// IntentQuery 鏈上唯讀查詢：餘額／行情／交易／池／狀態／區塊等。
	IntentQuery Intent = "query"
	// IntentAction 鏈上寫操作準備：兌換／存款／借款等（仍需使用者確認閘）。
	IntentAction Intent = "action"
)

// actionKeywords 寫操作關鍵字（中英）。命中即視為 action。
var actionKeywords = []string{
	"swap", "兌換", "兑换", "換成", "换成", "exchange", "trade", "buy", "sell", "買", "卖", "賣",
	"deposit", "存款", "存入", "stake", "質押", "质押", "lend", "lending",
	"borrow", "借", "借入", "借幣", "借款",
	"transfer", "轉帳", "转账", "匯款", "汇款", "send", "發送", "发送",
}

// queryKeywords 鏈上唯讀查詢關鍵字（中英）。命中即視為 query。
var queryKeywords = []string{
	"memory", "記憶", "记忆", "history", "對話紀錄", "对话记录",
	"price", "行情", "幣價", "币价", "ticker", "多少錢", "多少钱", "價格", "价格", "股價", "股价",
	"recent", "最近交易", "最近的交易", "历史交易", "歷史交易", "tx", "transactions", "轉帳紀錄", "转账记录",
	"balance", "餘額", "余额", "多少", "钱", "錢", "持有", "閒置", "闲置", "閒著", "闲著", "asset", "assets",
	"pool", "流動性", "流动性", "liquidity",
	"address", "地址", "查地址",
	"status", "狀態", "状态", "節點", "节点", "網絡", "网络", "sync", "同步",
	"block", "區塊", "区块", "最新", "latest", "hash", "哈希", "出塊", "出块", "高度",
	"audit", "審計", "审计", "consistent", "帳本", "賬本", "账本", "守恆", "守恒", "ledger",
	"supply", "供應", "供应", "總量", "总量", "市值", "cap", "上限", "發行", "发行",
	"reward", "獎勵", "奖励", "收益", "分潤", "分润", "coinbase",
	"miner", "礦工", "矿工", "挖礦", "挖矿", "算力", "礦機", "矿机",
	// v165 聯網搜尋觸發的時事/總體行情（最新資訊類）。
	"新聞", "新闻", "news", "時事", "时事", "天氣", "天气", "weather", "突發", "突发",
	"headline", "股市", "大盤", "大盘", "美股", "港股", "global market", "stock market", "current events",
}

// containsAny 判斷 low（已轉小寫）是否命中任一關鍵字。
func containsAny(low string, keys []string) bool {
	for _, k := range keys {
		if strings.Contains(low, k) {
			return true
		}
	}
	return false
}

// codeKeywords 程式開發/技術關鍵字（中英）。命中即視為「程式問題」，直接 chat 回答。
var codeKeywords = []string{
	"寫程式", "写程式", "寫代碼", "写代码", "寫代码", "程式碼", "代码", "这代码", "這段", "這段code",
	"報錯", "报错", "除錯", "调试", "调试", "debug", "演算法", "算法", "資料結構", "数据结构",
	"函式", "函数", "物件導向", "面向对象", "查詢語法", "語法", "语法",
	"write code", "write a function", "program", "coding", "function", "bug", "debug", "debugging",
	"error", "compile", "compiler", "runtime", "refactor", "refactoring", "algorithm", "data structure",
	"sql", "query", "select ", "golang", "go 語言", "go语言", "goroutine", "channel", "concurrency",
	"python", "javascript", "typescript", "java ", "rust", "react",
	"docker", "github", "api", "endpoint", "stack trace", "exception",
}

// codeSymbols 程式碼片段特徵符號/關鍵建構。
var codeSymbols = []string{"{", "}", ";", "=>", ":=", "->", "def ", "func ", "class ", "return ",
	"print(", "console.log", "SELECT ", "CREATE TABLE", "INSERT ", "fmt.Println", "if err !="}

// looksLikeCode 是否為程式/技術問題（含明確程式關鍵字，或多個程式碼符號特徵）。
func looksLikeCode(text string) bool {
	low := strings.ToLower(text)
	if containsAny(low, codeKeywords) {
		return true
	}
	hits := 0
	for _, s := range codeSymbols {
		if strings.Contains(low, strings.ToLower(s)) {
			hits++
		}
	}
	return hits >= 2
}

// ClassifyIntent 分類使用者訊息。
// 順序：先認程式問題（chat，避免 pool/借 等字誤判）→ 再 action（最具體寫意圖）→ query → chat。
func ClassifyIntent(text string) Intent {
	low := strings.TrimSpace(text)
	if low == "" {
		return IntentChat
	}
	// 程式/技術問題一律直接回答，不走鏈工具。
	if looksLikeCode(low) {
		return IntentChat
	}
	low = strings.ToLower(low)
	if containsAny(low, actionKeywords) {
		return IntentAction
	}
	if containsAny(low, queryKeywords) {
		return IntentQuery
	}
	return IntentChat
}

// detectLang 輕量語言偵測：含 CJK 統一表意文字 → "zh"，否則 "en"。
// 只用於離線人性化回應的語言選擇，不影響真模型（真模型自行跟隨使用者語言）。
func detectLang(text string) string {
	for _, r := range text {
		// CJK Unified Ideographs 基本區 + 擴充A（涵蓋常用繁簡字）。
		if unicode.Is(unicode.Han, r) {
			return "zh"
		}
	}
	return "en"
}

// chainTopic 代表從記憶中辨識出的「最近鏈上主題」，用於續問/指代接續。
type chainTopic string

const (
	topicNone    chainTopic = ""
	topicPrice   chainTopic = "price"
	topicBalance chainTopic = "balance"
	topicTxs     chainTopic = "txs"
)

// lastChainTopic 掃描對話歷史（由新到舊），回傳最近一次 assistant 處理過的鏈上主題。
// 規則式 provider 的 synthesize 輸出含固定前綴（見 provider_rules.go），此處與之比對。
func lastChainTopic(history []ChatMessage) chainTopic {
	for i := len(history) - 1; i >= 0; i-- {
		m := history[i]
		if m.Role != "assistant" {
			continue
		}
		c := m.Content
		switch {
		case strings.HasPrefix(c, "Market price:"):
			return topicPrice
		case strings.HasPrefix(c, "On-chain balance:"):
			return topicBalance
		case strings.HasPrefix(c, "Recent transactions:"):
			return topicTxs
		}
	}
	return topicNone
}

// followUpMarkers 續問/指代線索：短句或代詞/接續詞。
var followUpMarkers = []string{
	"它", "他", "那", "這個", "这个", "那個", "那个", "還", "还", "再", "也", "然後", "然后", "另外",
	"it", "that", "this", "then", "also", "still", "what about", "how about", "再來", "再来",
}

// looksLikeFollowUp 是否為「承接上一輪」的短句（語意依賴上文）。
// 僅在不含明確鏈上關鍵字、且非純問候/道謝/道別時才成立，避免搶走顯式查詢或獨立對話。
func looksLikeFollowUp(text string) bool {
	trimmed := strings.TrimSpace(text)
	low := strings.ToLower(trimmed)
	if low == "" {
		return false
	}
	if containsAny(low, actionKeywords) || containsAny(low, queryKeywords) {
		return false // 已有明確意圖，不需上下文推斷
	}
	if isChitChatOnly(low) {
		return false // 純問候/道謝/道別是獨立對話，不承接上一輪查詢
	}
	// 明確接續詞 → 視為續問。
	if containsAny(low, followUpMarkers) {
		return true
	}
	// 短問句且帶疑問語氣 → 視為續問。
	if len([]rune(trimmed)) <= 10 && strings.ContainsAny(trimmed, "嗎呢？?么嘛") {
		return true
	}
	return false
}

// isChitChatOnly 是否為單純的問候／道謝／道別（去掉標點後完全等於該詞）。
func isChitChatOnly(low string) bool {
	core := strings.Trim(low, " !!.。，,~～")
	chit := []string{
		"你好", "您好", "哈囉", "哈喽", "嗨", "早安", "晚安", "早上好", "下午好",
		"hello", "hi", "hey", "good morning", "good evening",
		"謝謝", "谢谢", "感謝", "感谢", "thanks", "thank you", "thx",
		"再見", "再见", "bye", "拜拜", "goodbye",
	}
	for _, c := range chit {
		if core == c {
			return true
		}
	}
	return false
}

// resolveIntent 給定本輪訊息與歷史，回傳最終意圖（chat 但為鏈上續問 → 視為 query）。
func resolveIntent(text string, history []ChatMessage) Intent {
	it := ClassifyIntent(text)
	if it != IntentChat {
		return it
	}
	if looksLikeFollowUp(text) && lastChainTopic(history) != topicNone {
		return IntentQuery
	}
	return IntentChat
}
