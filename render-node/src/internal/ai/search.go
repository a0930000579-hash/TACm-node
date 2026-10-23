package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// v165 追加：聯網搜尋（grounding）＋多語言檢索。
//
// 設計原則：
//   - SearchProvider 為可插拔介面：免金鑰優先（自架 SearXNG 類 JSON 端點，env 設定），
//     保留未來接付費金鑰 API（Google CSE / Bing / Serper 等）的介面；
//   - 用 goroutine＋context 並行召回（中文問題同時 zh+en），部分失敗容忍、全部失敗才降級；
//   - 完整 error handling、不 panic、不吞錯；
//   - 離線/無端點/連外失敗 → 明確回錯，由上層「誠實降級」，絕不報假來源、不編造連結。

// SearchResult 單筆搜尋結果（給模型與前端引用來源）。
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// Citation 是回應給前端的引用來源（與 SearchResult 同源，欄位固定）。
type Citation struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// SearchProvider 可插拔搜尋提供者。
type SearchProvider interface {
	// Search 以 query/lang 召回結果；lang 為 zh|en（提示端點語言）。
	Search(ctx context.Context, query, lang string) ([]SearchResult, error)
	// Fetch 抓取指定 URL 正文（已去 HTML/script、截長）。
	Fetch(ctx context.Context, rawURL string) (string, error)
	// Name 提供者名稱（searxng | none | fake-search）。
	Name() string
}

// ---------------------------------------------------------------------------
// 免金鑰 HTTP 實作（SearXNG 相容 JSON：GET {endpoint}/search?format=json&q=..&language=..）。
// ---------------------------------------------------------------------------

// HTTPSearchProvider 對自架 SearXNG 類實例的客戶端。endpoint 為空＝未設定＝永遠回錯（降級）。
type HTTPSearchProvider struct {
	endpoint string
	cli      *http.Client
}

// NewHTTPSearchProvider 建立客戶端；endpoint 空代表「未設定」。
func NewHTTPSearchProvider(endpoint string) *HTTPSearchProvider {
	return &HTTPSearchProvider{
		endpoint: strings.TrimRight(strings.TrimSpace(endpoint), "/"),
		cli:      &http.Client{Timeout: 12 * time.Second},
	}
}

// NewHTTPSearchProviderFromEnv 從 TAC_SEARCH_URL 讀端點；未設定則回「永遠降級」的 provider。
func NewHTTPSearchProviderFromEnv() *HTTPSearchProvider {
	return NewHTTPSearchProvider(os.Getenv("TAC_SEARCH_URL"))
}

// Name 提供者名稱。
func (p *HTTPSearchProvider) Name() string {
	if p.endpoint == "" {
		return "none"
	}
	return "searxng"
}

// searxngResp SearXNG /search?format=json 回應。
type searxngResp struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
	} `json:"results"`
}

// Search 實作：GET /search?format=json。
func (p *HTTPSearchProvider) Search(ctx context.Context, query, lang string) ([]SearchResult, error) {
	if p.endpoint == "" {
		return nil, fmt.Errorf("search endpoint not configured (set TAC_SEARCH_URL)")
	}
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("empty search query")
	}
	u := fmt.Sprintf("%s/search?format=json&language=%s&q=%s", p.endpoint, url.QueryEscape(lang), url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("build search req: %w", err)
	}
	resp, err := p.cli.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call search: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search HTTP %d", resp.StatusCode)
	}
	var out searxngResp
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode search: %w", err)
	}
	results := make([]SearchResult, 0, len(out.Results))
	for _, r := range out.Results {
		if r.URL == "" {
			continue
		}
		results = append(results, SearchResult{Title: r.Title, URL: r.URL, Snippet: r.Content})
	}
	return results, nil
}

// fetchMaxChars 抓取正文長度上限。
const fetchMaxChars = 4000

var (
	reStripTags  = regexp.MustCompile(`(?s)<script.*?</script>|<style.*?</style>`)
	reAnyTag     = regexp.MustCompile(`<[^>]+>`)
	reMultiSpace = regexp.MustCompile(`[ \t]+`)
	reBlankLine  = regexp.MustCompile(`\n{3,}`)
)

// Fetch 實作：抓 HTML、去 script/style/標籤、折行、截長。
func (p *HTTPSearchProvider) Fetch(ctx context.Context, rawURL string) (string, error) {
	if p.endpoint == "" && !strings.HasPrefix(rawURL, "http") {
		return "", fmt.Errorf("search endpoint not configured")
	}
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return "", fmt.Errorf("invalid url: %s", rawURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("build fetch req: %w", err)
	}
	req.Header.Set("User-Agent", "TacmAiBot/1.0 (+on-chain assistant)")
	resp, err := p.cli.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch url: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	html := string(body)
	html = reStripTags.ReplaceAllString(html, " ")
	html = reAnyTag.ReplaceAllString(html, " ")
	html = reMultiSpace.ReplaceAllString(html, " ")
	html = strings.ReplaceAll(html, "\r\n", "\n")
	html = reBlankLine.ReplaceAllString(html, "\n\n")
	out := strings.TrimSpace(html)
	if len(out) > fetchMaxChars {
		out = out[:fetchMaxChars] + "…"
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 多語言並行召回（goroutine＋channel；部分失敗容忍、全部失敗才降級）。
// ---------------------------------------------------------------------------

// SearchOutcome RunSearch 的回傳：合併去重後的結果、實際下過的查詢、錯誤（nil=全部成功；partial=true 時為部分失敗摘要）。
type SearchOutcome struct {
	Results []SearchResult
	Queries []string
	Partial bool
}

// WebUnavailableNote 離線/未設端點/逾時時的誠實降級訊息（跟隨使用者語言，不編造來源）。
func WebUnavailableNote(lang string) string {
	if lang == "zh" {
		return "目前無法連線搜尋（離線或未設定搜尋端點）。我改用既有知識回答，不會編造任何來源或連結。"
	}
	return "Web search is currently unavailable (offline / no search endpoint). I will answer from existing knowledge without fabricating any source or link."
}

// RunSearch 依使用者語言決定召回語種：中文問題同時以 zh＋en 並行召回，英文問題單 en。
// 以 URL 去重合併；任一語種成功即視為可用（Partial 標記部分失敗）；全部失敗才回 error。
func RunSearch(ctx context.Context, sp SearchProvider, query, lang string) (*SearchOutcome, error) {
	if sp == nil {
		return nil, fmt.Errorf("nil search provider")
	}
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("empty query")
	}
	if lang == "" {
		lang = detectLang(query)
	}
	langs := []string{lang}
	if lang == "zh" {
		// 中文問題：同時以中文＋英文召回，提升覆蓋。
		langs = []string{"zh", "en"}
	}

	type resCh struct {
		lang    string
		results []SearchResult
		err     error
	}
	ch := make(chan resCh, len(langs))
	for _, l := range langs {
		l := l
		go func() {
			rs, err := sp.Search(ctx, query, l)
			ch <- resCh{lang: l, results: rs, err: err}
		}()
	}

	seen := map[string]bool{}
	out := &SearchOutcome{}
	var firstErr error
	success := 0
	for i := 0; i < len(langs); i++ {
		r := <-ch
		out.Queries = append(out.Queries, query+" ["+r.lang+"]")
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		success++
		for _, it := range r.results {
			if it.URL == "" || seen[it.URL] {
				continue
			}
			seen[it.URL] = true
			out.Results = append(out.Results, it)
		}
	}
	out.Partial = success > 0 && success < len(langs)
	if success == 0 {
		// 全部失敗 → 降級（上層不得報假來源）。
		return nil, fmt.Errorf("all search calls failed: %w", firstErr)
	}
	return out, nil
}
