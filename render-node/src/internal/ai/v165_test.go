package ai

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// v165：通用、人性化 AI 助手測試。
// 分類：happy（一般閒聊）、general（非鏈問題不調工具）、tool（鏈上查詢進工具流程＋風險提示）、
// error（Provider 失敗 → 離線 fallback 人性化回應）。

func mustRegPriceBalanceTxs(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	r.MustRegister(&Tool{Name: "get_price", Run: func(ctx context.Context, args map[string]any) (ToolResult, error) {
		return ToolResult{Summary: "TAC/USDT=0.12", Data: map[string]any{"symbol": "TAC/USDT"}}, nil
	}})
	r.MustRegister(&Tool{Name: "get_balance", Run: func(ctx context.Context, args map[string]any) (ToolResult, error) {
		return ToolResult{Summary: "1234.5 TAC", Data: map[string]any{"balance": "1234.5"}}, nil
	}})
	r.MustRegister(&Tool{Name: "get_recent_txs", Run: func(ctx context.Context, args map[string]any) (ToolResult, error) {
		return ToolResult{Summary: "2 txs"}, nil
	}})
	return r
}

// happy：一般問候 → 人性化回答、不呼叫任何工具、intent=chat。
func TestV165_HappyChitChatNoTool(t *testing.T) {
	reg := mustRegPriceBalanceTxs(t)
	called := map[string]bool{}
	for _, tk := range reg.List() {
		orig := tk.Run
		tk.Run = func(ctx context.Context, args map[string]any) (ToolResult, error) {
			called[tk.Name] = true
			return orig(ctx, args)
		}
	}
	agent, err := NewAgent(NewRulesProvider("0xabc"), reg)
	if err != nil {
		t.Fatal(err)
	}
	res, err := agent.Run(context.Background(), "你好呀", "v165-happy", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(res.Answer) == "" {
		t.Fatal("expected non-empty human answer")
	}
	if len(called) != 0 {
		t.Fatalf("chit-chat MUST NOT call tools, got %v", called)
	}
	if res.Intent != string(IntentChat) {
		t.Fatalf("expected intent=chat, got %q", res.Intent)
	}
	// 回答應為中文（含 CJK）且非空。
	if !containsCJK(res.Answer) {
		t.Fatalf("expected Traditional Chinese reply, got %q", res.Answer)
	}
}

// general：非鏈問題（寫作/解釋/翻譯）→ 不強行呼叫工具、intent=chat。
func TestV165_GeneralNonChainNoTool(t *testing.T) {
	cases := []string{
		"幫我寫一封週報",
		"解釋什麼是 PoS",
		"把這句話翻成英文",
		"我想規劃這週的讀書計畫",
	}
	for _, msg := range cases {
		reg := mustRegPriceBalanceTxs(t)
		ran := false
		for _, tk := range reg.List() {
			orig := tk.Run
			tk.Run = func(ctx context.Context, args map[string]any) (ToolResult, error) {
				ran = true
				return orig(ctx, args)
			}
		}
		agent, _ := NewAgent(NewRulesProvider("0xabc"), reg)
		res, err := agent.Run(context.Background(), msg, "v165-gen", false)
		if err != nil {
			t.Fatalf("%q: %v", msg, err)
		}
		if ran {
			t.Fatalf("%q MUST NOT call on-chain tools", msg)
		}
		if res.Intent != string(IntentChat) {
			t.Fatalf("%q: expected intent=chat, got %q", msg, res.Intent)
		}
		if strings.TrimSpace(res.Answer) == "" {
			t.Fatalf("%q: empty answer", msg)
		}
	}
}

// general-en：英文一般問題 → 英文人性化回答。
func TestV165_GeneralEnglishReply(t *testing.T) {
	agent, _ := NewAgent(NewRulesProvider("0xabc"), mustRegPriceBalanceTxs(t))
	res, err := agent.Run(context.Background(), "What can you help me with?", "v165-en", false)
	if err != nil {
		t.Fatal(err)
	}
	if containsCJK(res.Answer) {
		t.Fatalf("expected English reply, got %q", res.Answer)
	}
	if !strings.Contains(strings.ToLower(res.Answer), "i can help") &&
		!strings.Contains(strings.ToLower(res.Answer), "on-chain") {
		t.Fatalf("unexpected help reply: %q", res.Answer)
	}
}

// tool：鏈上查詢正確進工具流程；寫操作停在確認閘；回答帶風險提示。
func TestV165_ToolChainQuery(t *testing.T) {
	reg := NewRegistry()
	reg.MustRegister(&Tool{Name: "get_balance", Run: func(ctx context.Context, args map[string]any) (ToolResult, error) {
		return ToolResult{Summary: "1234.5 TAC", Data: map[string]any{"balance": "1234.5"}}, nil
	}})
	agent, _ := NewAgent(NewRulesProvider("0xabc"), reg)
	res, err := agent.Run(context.Background(), "what is my balance", "v165-tool", false)
	if err != nil {
		t.Fatal(err)
	}
	var sawTool bool
	for _, s := range res.Trace {
		if s.Kind == "tool" && s.ToolName == "get_balance" {
			sawTool = true
		}
	}
	if !sawTool {
		t.Fatalf("expected get_balance tool step, trace=%+v", res.Trace)
	}
	if res.Intent != string(IntentQuery) {
		t.Fatalf("expected intent=query, got %q", res.Intent)
	}
	// 風險提示：synthesize 結尾自帶 not investment advice。
	if !strings.Contains(strings.ToLower(res.Answer), "not investment advice") {
		t.Fatalf("expected risk note in answer, got %q", res.Answer)
	}
}

// tool-action：寫操作 → needs_confirm，且不執行。
func TestV165_ToolWriteConfirmGate(t *testing.T) {
	reg := NewRegistry()
	executed := false
	reg.MustRegister(&Tool{Name: "dex_swap", RequiresConfirm: true, Run: func(ctx context.Context, args map[string]any) (ToolResult, error) {
		executed = true
		return ToolResult{Summary: "swapped"}, nil
	}})
	agent, _ := NewAgent(NewRulesProvider("0xabc"), reg)
	res, err := agent.Run(context.Background(), "please swap 100 TAC to stablecoin", "v165-swap", false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.NeedsConfirm {
		t.Fatal("expected NeedsConfirm")
	}
	if executed {
		t.Fatal("write tool MUST NOT execute without confirmation")
	}
	if res.Intent != string(IntentAction) {
		t.Fatalf("expected intent=action, got %q", res.Intent)
	}
}

// context：多輪上下文串接——先問行情，再用指代短句續問，應自動接回 get_price。
func TestV165_MultiTurnContextChaining(t *testing.T) {
	reg := mustRegPriceBalanceTxs(t)
	agent, _ := NewAgent(NewRulesProvider("0xabc"), reg)

	turn1, err := agent.Run(context.Background(), "TAC price", "v165-ctx", false)
	if err != nil {
		t.Fatal(err)
	}
	var sawPrice1 bool
	for _, s := range turn1.Trace {
		if s.ToolName == "get_price" {
			sawPrice1 = true
		}
	}
	if !sawPrice1 {
		t.Fatalf("turn1 should call get_price, trace=%+v", turn1.Trace)
	}

	// 第二輪：指代續問，不重述幣種。
	turn2, err := agent.Run(context.Background(), "那它現在呢?", "v165-ctx", false)
	if err != nil {
		t.Fatal(err)
	}
	var sawPrice2 bool
	for _, s := range turn2.Trace {
		if s.ToolName == "get_price" {
			sawPrice2 = true
		}
	}
	if !sawPrice2 {
		t.Fatalf("turn2 follow-up should chain back to get_price, trace=%+v answer=%q", turn2.Trace, turn2.Answer)
	}
	if turn2.Intent != string(IntentQuery) {
		t.Fatalf("turn2 follow-up should be intent=query, got %q", turn2.Intent)
	}
}

// error：primary（會失敗）provider → FallbackProvider 退回規則式，仍給人性化回答（非模板）。
type failingProvider struct{}

func (f *failingProvider) Name() string { return "broken-llm" }
func (f *failingProvider) Complete(ctx context.Context, msgs []ChatMessage, tools []*Tool) (Turn, error) {
	return Turn{}, errors.New("llm offline: connection refused")
}

func TestV165_ErrorFallbackHumanized(t *testing.T) {
	base := NewRulesProvider("0xabc")
	fb := NewFallbackProvider(&failingProvider{}, base)

	// 第一次就失敗 → fallback。
	turn, err := fb.Complete(context.Background(), []ChatMessage{{Role: "user", Content: "你能幫我什麼？"}}, nil)
	if err != nil {
		t.Fatalf("fallback should not error: %v", err)
	}
	if strings.TrimSpace(turn.Answer) == "" {
		t.Fatal("fallback answer must be humanized/non-empty")
	}
	// 應為幫助類人性化回覆（含能力說明），而非空字串或 panic。
	if !containsCJK(turn.Answer) {
		t.Fatalf("expected zh help reply, got %q", turn.Answer)
	}
}

// fallback 人性化：不同子意圖回覆不同，且包含追問/引導（非單一固定模板）。
func TestV165_GeneralReplyVaries(t *testing.T) {
	p := NewRulesProvider("0xabc")
	greeting := p.generalReply("你好", "zh", nil)
	writing := p.generalReply("幫我寫信", "zh", nil)
	translate := p.generalReply("translate this", "en", nil)
	if greeting == writing || greeting == translate {
		t.Fatalf("replies should vary by sub-intent: greeting=%q writing=%q translate=%q", greeting, writing, translate)
	}
	if writing == translate {
		t.Fatalf("zh/en replies should differ: writing=%q translate=%q", writing, translate)
	}
}

// 意圖分類單元測試。
func TestV165_IntentClassification(t *testing.T) {
	cases := []struct {
		msg  string
		want Intent
	}{
		{"你好", IntentChat},
		{"幫我寫一封週報", IntentChat},
		{"explain PoS", IntentChat},
		{"what is my balance", IntentQuery},
		{"TAC price", IntentQuery},
		{"show recent transactions", IntentQuery},
		{"please swap to stablecoin", IntentAction},
		{"deposit TAC", IntentAction},
		{"borrow USDT", IntentAction},
	}
	for _, c := range cases {
		if got := ClassifyIntent(c.msg); got != c.want {
			t.Fatalf("ClassifyIntent(%q)=%q want %q", c.msg, got, c.want)
		}
	}
}

// 語言偵測。
func TestV165_DetectLang(t *testing.T) {
	if detectLang("你好世界") != "zh" {
		t.Fatal("CJK should detect as zh")
	}
	if detectLang("hello world") != "en" {
		t.Fatal("latin should detect as en")
	}
}

// containsCJK 輔助：是否含中文字元。
func containsCJK(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}

// v165 最終補強：四類意圖分類（中文日常/英文日常/程式碼/鏈上操作）。
// 前三類 = IntentChat 且不選鏈工具、離線給實質回答（程式碼含 fence）；後者 = Query 且選鏈工具。
func TestV165_FourCategoryIntent(t *testing.T) {
	zhDaily := "我今天睡不好怎麼辦"
	enDaily := "give me a quick healthy breakfast idea"
	codeGo := "用 Go 寫一個 goroutine pool 附說明"
	codeSQL := "why this SQL error: SELECT FROM users"
	chain := "我的錢包餘額"

	// 分類斷言：前三 = chat，鏈上 = query。
	for _, q := range []string{zhDaily, enDaily, codeGo, codeSQL} {
		if got := ClassifyIntent(q); got != IntentChat {
			t.Fatalf("%q should be IntentChat, got %s", q, got)
		}
	}
	if got := ClassifyIntent(chain); got != IntentQuery {
		// balance 關鍵字應為 query
		t.Fatalf("%q should be IntentQuery, got %s", chain, got)
	}

	// 空 registry：日常/程式問題不應試圖呼叫鏈工具，直接給離線回答。
	ag, _ := NewAgent(NewRulesProvider("0xabc"), NewRegistry())
	for _, q := range []string{zhDaily, enDaily, codeGo, codeSQL} {
		res, err := ag.Run(context.Background(), q, "s1", false)
		if err != nil {
			t.Fatal(err)
		}
		if res.Intent != "chat" {
			t.Fatalf("%q intent=%s want chat", q, res.Intent)
		}
		for _, tr := range res.Trace {
			if tr.Kind == "tool" && tr.ToolName != "web_search" && tr.ToolName != "web_fetch" {
				t.Fatalf("%q should not call chain tool %s", q, tr.ToolName)
			}
		}
	}

	// 程式回答含 fenced code block（Go 與 SQL 各一）。
	resGo, _ := ag.Run(context.Background(), codeGo, "s2", false)
	if !strings.Contains(resGo.Answer, "```") {
		t.Fatalf("go code reply should contain fenced block, got %q", resGo.Answer)
	}
	resSQL, _ := ag.Run(context.Background(), codeSQL, "s3", false)
	if !strings.Contains(resSQL.Answer, "```sql") {
		t.Fatalf("sql reply should contain sql fence, got %q", resSQL.Answer)
	}

	// 鏈上：含 get_balance registry → 應選鏈工具。
	reg := NewRegistry()
	reg.MustRegister(&Tool{Name: "get_balance", Run: func(ctx context.Context, args map[string]any) (ToolResult, error) {
		return ToolResult{Summary: "balance: 12.5 TAC"}, nil
	}})
	ag2, _ := NewAgent(NewRulesProvider("0xabc"), reg)
	resBal, err := ag2.Run(context.Background(), chain, "s4", false)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	for _, tr := range resBal.Trace {
		if tr.Kind == "tool" && tr.ToolName == "get_balance" {
			called = true
		}
	}
	if !called {
		t.Fatalf("balance should call get_balance, trace=%v", resBal.Trace)
	}
}
