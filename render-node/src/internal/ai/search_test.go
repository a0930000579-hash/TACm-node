package ai

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

// v165：聯網搜尋 grounding 測試。全部使用可注入假 SearchProvider，不依賴真實外網。

type fakeSearch struct {
	results map[string][]SearchResult
	errFor  map[string]error
	mu      sync.Mutex
	calls   []string
}

func (f *fakeSearch) Name() string { return "fake-search" }
func (f *fakeSearch) Search(ctx context.Context, query, lang string) ([]SearchResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, lang)
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err // 模擬真實 provider 遵守 context 取消
	}
	if e, ok := f.errFor[lang]; ok {
		return nil, e
	}
	return f.results[lang], nil
}

func (f *fakeSearch) callsSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}
func (f *fakeSearch) Fetch(ctx context.Context, rawURL string) (string, error) {
	return "body of " + rawURL, nil
}

func dedupURLs(rs []SearchResult) map[string]bool {
	m := map[string]bool{}
	for _, r := range rs {
		m[r.URL] = true
	}
	return m
}

// 中文問題：同時 zh+en 召回，依 URL 去重合併。
func TestRunSearch_ZhMergesAndDedupes(t *testing.T) {
	sp := &fakeSearch{results: map[string][]SearchResult{
		"zh": {{Title: "中1", URL: "http://a"}, {Title: "中2", URL: "http://b"}},
		"en": {{Title: "en1", URL: "http://a"}, {Title: "en2", URL: "http://c"}},
	}}
	out, err := RunSearch(context.Background(), sp, "今天新聞", "zh")
	if err != nil {
		t.Fatal(err)
	}
	// 應同時下過 zh 與 en。
	sawZh, sawEn := false, false
	for _, c := range sp.callsSnapshot() {
		if c == "zh" {
			sawZh = true
		}
		if c == "en" {
			sawEn = true
		}
	}
	if !sawZh || !sawEn {
		t.Fatalf("zh query should recall zh+en, calls=%v", sp.callsSnapshot())
	}
	// URL 去重：http://a 只計一次，共 a/b/c 三筆。
	urls := dedupURLs(out.Results)
	if !urls["http://a"] || !urls["http://b"] || !urls["http://c"] || len(urls) != 3 {
		t.Fatalf("expected deduped a/b/c, got %v", urls)
	}
	if len(out.Queries) != 2 {
		t.Fatalf("expected 2 query labels, got %v", out.Queries)
	}
	if out.Partial {
		t.Fatal("both langs succeeded, should not be partial")
	}
}

// 英文問題：只召回 en。
func TestRunSearch_EnSingleLang(t *testing.T) {
	sp := &fakeSearch{results: map[string][]SearchResult{
		"en": {{Title: "e1", URL: "http://x"}},
	}}
	out, err := RunSearch(context.Background(), sp, "global markets today", "en")
	if err != nil {
		t.Fatal(err)
	}
	cs := sp.callsSnapshot()
	if len(cs) != 1 || cs[0] != "en" {
		t.Fatalf("en query should recall en only, calls=%v", cs)
	}
	if len(out.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(out.Results))
	}
}

// 部分失敗：一語種失敗、另一成功 → Partial=true 仍回結果。
func TestRunSearch_PartialFailure(t *testing.T) {
	sp := &fakeSearch{
		results: map[string][]SearchResult{"en": {{Title: "e1", URL: "http://x"}}},
		errFor:  map[string]error{"zh": errors.New("zh endpoint down")},
	}
	out, err := RunSearch(context.Background(), sp, "新聞", "zh")
	if err != nil {
		t.Fatalf("partial failure should still return results: %v", err)
	}
	if !out.Partial {
		t.Fatal("expected Partial=true")
	}
	if len(out.Results) != 1 {
		t.Fatalf("expected en result, got %v", out.Results)
	}
}

// 全部失敗 → error（上層降級，不報假來源）。
func TestRunSearch_AllFail(t *testing.T) {
	sp := &fakeSearch{errFor: map[string]error{"zh": errors.New("down"), "en": errors.New("down")}}
	if _, err := RunSearch(context.Background(), sp, "新聞", "zh"); err == nil {
		t.Fatal("expected error when all search calls fail")
	}
}

// context 取消 → 回錯誤（不 panic、不吞錯）。
func TestRunSearch_ContextCancel(t *testing.T) {
	sp := &fakeSearch{results: map[string][]SearchResult{"en": {{Title: "x", URL: "http://x"}}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RunSearch(ctx, sp, "news", "en"); err == nil {
		t.Fatal("expected error on cancelled context")
	}
}

// 意圖：新聞類 → web_search；鏈上餘額 → 不亂搜。
func TestRules_NewsRoutesWebSearch(t *testing.T) {
	p := NewRulesProvider("0xabc")
	turn, err := p.Complete(context.Background(), []ChatMessage{{Role: "user", Content: "今天有什麼國際新聞？"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !turn.HasToolCalls() || turn.ToolCalls[0].Name != "web_search" {
		t.Fatalf("news should route to web_search, got %+v", turn.ToolCalls)
	}
	if q, _ := turn.ToolCalls[0].Args["query"].(string); !strings.Contains(q, "新聞") {
		t.Fatalf("query should echo user text, got %v", turn.ToolCalls[0].Args)
	}

	// 鏈上問題不應走 web_search。
	turn2, _ := p.Complete(context.Background(), []ChatMessage{{Role: "user", Content: "what is my balance"}}, nil)
	for _, c := range turn2.ToolCalls {
		if c.Name == "web_search" {
			t.Fatal("on-chain balance query must NOT trigger web search")
		}
	}
}

// Agent 整合：新聞問題 → web_search 執行後 Result.Searched 與 Citations 填入。
func newFakeWebRegistry(sp SearchProvider) *Registry {
	r := NewRegistry()
	r.MustRegister(&Tool{Name: "web_search", Run: func(ctx context.Context, args map[string]any) (ToolResult, error) {
		q, _ := args["query"].(string)
		lang, _ := args["lang"].(string)
		if lang == "" {
			lang = "en"
		}
		out, err := RunSearch(ctx, sp, q, lang)
		if err != nil {
			// 與正式工具一致：回本地化降級訊息，不編造來源。
			return ToolResult{Summary: WebUnavailableNote(lang), Data: map[string]any{"queries": []string{q}, "citations": []SearchResult{}}}, nil
		}
		return ToolResult{Summary: "ok", Data: map[string]any{"queries": out.Queries, "citations": out.Results}}, nil
	}})
	return r
}

func TestAgent_WebSearchCitations(t *testing.T) {
	sp := &fakeSearch{results: map[string][]SearchResult{
		"zh": {{Title: "標題甲", URL: "http://a"}},
		"en": {{Title: "Title B", URL: "http://b"}},
	}}
	agent, err := NewAgent(NewRulesProvider("0xabc"), newFakeWebRegistry(sp))
	if err != nil {
		t.Fatal(err)
	}
	res, err := agent.Run(context.Background(), "今天國際新聞", "srch1", false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Searched {
		t.Fatalf("expected Searched=true, answer=%q", res.Answer)
	}
	if len(res.Citations) < 2 {
		t.Fatalf("expected citations from zh+en, got %v", res.Citations)
	}
	// 引用應含真實網址，不得為空。
	for _, c := range res.Citations {
		if c.URL == "" {
			t.Fatalf("citation missing url: %+v", c)
		}
	}
}

// v165 補正：離線降級訊息跟隨使用者語言（zh→中文、en→英文），且不編造來源。
func TestAgent_WebDegradeFollowsLanguage(t *testing.T) {
	// 中文提問 → 降級訊息應為中文，且不出現英文降級句。
	zhSp := &fakeSearch{errFor: map[string]error{"zh": errors.New("offline"), "en": errors.New("offline")}}
	zhAgent, _ := NewAgent(NewRulesProvider("0xabc"), newFakeWebRegistry(zhSp))
	zhRes, err := zhAgent.Run(context.Background(), "今天有什麼新聞", "lang-zh", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(zhRes.Answer, "目前無法連線搜尋") {
		t.Fatalf("zh degrade should be Chinese, got %q", zhRes.Answer)
	}
	if strings.Contains(zhRes.Answer, "Web search is currently unavailable") {
		t.Fatalf("zh degrade should NOT leak English sentence, got %q", zhRes.Answer)
	}
	if zhRes.Searched || len(zhRes.Citations) != 0 {
		t.Fatal("zh degrade must claim no sources")
	}

	// 英文提問 → 降級訊息應為英文。
	enSp := &fakeSearch{errFor: map[string]error{"en": errors.New("offline")}}
	enAgent, _ := NewAgent(NewRulesProvider("0xabc"), newFakeWebRegistry(enSp))
	enRes, err := enAgent.Run(context.Background(), "any news today", "lang-en", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(enRes.Answer, "Web search is currently unavailable") {
		t.Fatalf("en degrade should be English, got %q", enRes.Answer)
	}
}

// v165 補正：無搜尋結果時空欄位序列化為 []/false（非 null）。
func TestResult_EmptyFieldsSerializeAsArrays(t *testing.T) {
	res := &Result{Answer: "hi", Queries: []string{}, Citations: []Citation{}}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"queries":[]`) {
		t.Fatalf("queries should serialize as [], got %s", s)
	}
	if !strings.Contains(s, `"citations":[]`) {
		t.Fatalf("citations should serialize as [], got %s", s)
	}
	if !strings.Contains(s, `"searched":false`) {
		t.Fatalf("searched should be false, got %s", s)
	}
}

// 降級：搜尋全部失敗 → 不宣稱有來源（Searched=false、citations 空）。
func TestAgent_WebSearchDegrades(t *testing.T) {
	sp := &fakeSearch{errFor: map[string]error{"zh": errors.New("offline"), "en": errors.New("offline")}}
	agent, _ := NewAgent(NewRulesProvider("0xabc"), newFakeWebRegistry(sp))
	res, err := agent.Run(context.Background(), "今天新聞", "srch2", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Searched {
		t.Fatal("degraded search must not claim sources")
	}
	if len(res.Citations) != 0 {
		t.Fatalf("degraded search must return no citations, got %v", res.Citations)
	}
	// 回答應誠實提及無法連線（中英文皆可）。
	low := strings.ToLower(res.Answer)
	if !strings.Contains(low, "unavailable") &&
		!strings.Contains(low, "offline") &&
		!strings.Contains(res.Answer, "無法連線") {
		t.Fatalf("expected honest degradation note, got %q", res.Answer)
	}
}
