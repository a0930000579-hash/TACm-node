// Package oracle：多數據源（multi-source）抽象層。
//
// 本檔定義去中心化喂價所需的 Source 介面與數個內建實作：
//   - FuncSource：可注入 fetcher（測試替身 / 自訂邏輯）。
//   - StaticSource：固定報價表（模擬源 / 離線 fallback）。
//   - RefSource：複用 internal/exchange 的主流幣參考行情（即時＋靜態）。
//   - HTTPSource：可配置 URL 的真實 HTTP 端點（含 timeout，測試不卡死）。
//
// 設計原則：單一 Source 故障（逾時/錯誤）只影響自身，不影響其他源聚合。
package oracle

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"tacm/internal/exchange"
)

// Source 為一個獨立價格數據源。聚合層對每個 symbol 呼叫 Fetch 收集報價，
// 任何 err 都視為該源本輪不可用（標 stale/error），不panic、不影響其他源。
type Source interface {
	// Name 回傳唯一源名（小寫識別用，如 "binance"）。
	Name() string
	// Weight 回傳加權平均權重（>0；中位數聚合時不使用，僅加權平均用）。
	Weight() float64
	// Fetch 拉取指定 symbols 的最新報價（symbol → price）。
	// 回傳的 map 可只含本源有報價的 symbol；err 代表整源本輪失敗。
	// 實作必須遵守 ctx 取消/逾時，不可阻塞測試。
	Fetch(ctx context.Context, symbols []string) (map[string]float64, error)
}

// ---- FuncSource：可注入 fetcher（測試替身 / 自訂邏輯） ----

// FuncSource 以函式注入 Fetch 行為，便於測試與自訂源。
type FuncSource struct {
	NameV   string
	WeightV float64
	FetchFn func(ctx context.Context, symbols []string) (map[string]float64, error)
}

// Name 實作 Source。
func (f *FuncSource) Name() string { return f.NameV }

// Weight 實作 Source。
func (f *FuncSource) Weight() float64 { return f.WeightV }

// Fetch 實作 Source：直接委派注入函式。
func (f *FuncSource) Fetch(ctx context.Context, symbols []string) (map[string]float64, error) {
	if f.FetchFn == nil {
		return nil, fmt.Errorf("source %s: fetch fn not set", f.NameV)
	}
	return f.FetchFn(ctx, symbols)
}

// ---- StaticSource：固定報價表（模擬源 / 離線 fallback） ----

// StaticSource 回傳固定報價，用於離線測試與模擬第二/第三獨立源。
type StaticSource struct {
	NameV   string
	WeightV float64
	Px      map[string]float64 // 已大寫 symbol → price
}

// Name 實作 Source。
func (s *StaticSource) Name() string { return s.NameV }

// Weight 實作 Source。
func (s *StaticSource) Weight() float64 { return s.WeightV }

// Fetch 實作 Source：從固定表挑出請求的 symbols。
func (s *StaticSource) Fetch(_ context.Context, symbols []string) (map[string]float64, error) {
	out := make(map[string]float64, len(symbols))
	for _, sym := range symbols {
		u := strings.ToUpper(strings.TrimSpace(sym))
		if p, ok := s.Px[u]; ok && p > 0 {
			out[u] = p
		}
	}
	return out, nil
}

// ---- RefSource：複用 internal/exchange 主流幣參考行情 ----

// RefSource 包裝 exchange.RefMarkets（即時 Binance/CoinGecko ＋ 靜態 fallback）。
// 這是與既有 Oracle.refreshLocked 同源的「主參考源」，保證向後相容。
type RefSource struct {
	NameV   string
	WeightV float64
}

// Name 實作 Source。
func (r *RefSource) Name() string { return r.NameV }

// Weight 實作 Source。
func (r *RefSource) Weight() float64 { return r.WeightV }

// Fetch 實作 Source：呼叫 exchange.IsRefMarket 取最新行情。
func (r *RefSource) Fetch(_ context.Context, symbols []string) (map[string]float64, error) {
	out := make(map[string]float64, len(symbols))
	for _, sym := range symbols {
		u := strings.ToUpper(strings.TrimSpace(sym))
		rm, ok := exchange.IsRefMarket(u)
		if !ok || rm.Price <= 0 {
			continue
		}
		out[u] = rm.Price
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("ref source: no ref market quotes")
	}
	return out, nil
}

// ---- HTTPSource：可配置 URL 真實 HTTP 端點（含 timeout） ----

// HTTPSource 從可配置 JSON 端點拉報價。Parser 由建構時注入，
// 預設 parseBinancePrices 解析幣安 24hr ticker 陣列。
type HTTPSource struct {
	NameV   string
	WeightV float64
	URL     string
	Client  *http.Client
	// Parse 把 HTTP body 解析成 symbol→price；nil 用 parseBinancePrices。
	Parse func(body []byte) (map[string]float64, error)
}

// Name 實作 Source。
func (h *HTTPSource) Name() string { return h.NameV }

// Weight 實作 Source。
func (h *HTTPSource) Weight() float64 { return h.WeightV }

// Fetch 實作 Source：帶 ctx timeout GET，錯誤即回傳（聚合層降級處理）。
func (h *HTTPSource) Fetch(ctx context.Context, symbols []string) (map[string]float64, error) {
	if h.URL == "" {
		return nil, fmt.Errorf("source %s: url not configured", h.NameV)
	}
	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second} // 短 timeout，測試/主網都不卡死
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("source %s: http %d", h.NameV, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 上限 1MB 防暴量
	if err != nil {
		return nil, err
	}
	parse := h.Parse
	if parse == nil {
		parse = parseBinancePrices
	}
	all, err := parse(body)
	if err != nil {
		return nil, err
	}
	// 只保留請求的 symbols（大寫正規化）。
	want := make(map[string]struct{}, len(symbols))
	for _, s := range symbols {
		want[strings.ToUpper(strings.TrimSpace(s))] = struct{}{}
	}
	out := make(map[string]float64, len(all))
	for sym, p := range all {
		u := strings.ToUpper(strings.TrimSpace(sym))
		if _, ok := want[u]; ok && p > 0 {
			out[u] = p
		}
	}
	return out, nil
}

// parseBinancePrices 解析幣安 GET /api/v3/ticker/24hr 陣列。
// symbol 欄位如 "BTCUSDT" → "BTC/USDT"。
func parseBinancePrices(body []byte) (map[string]float64, error) {
	var arr []struct {
		Symbol    string `json:"symbol"`
		LastPrice string `json:"lastPrice"`
	}
	if err := json.Unmarshal(body, &arr); err != nil {
		return nil, err
	}
	symMap := map[string]string{
		"BTCUSDT": "BTC/USDT", "ETHUSDT": "ETH/USDT", "BNBUSDT": "BNB/USDT",
		"SOLUSDT": "SOL/USDT", "ADAUSDT": "ADA/USDT",
	}
	out := make(map[string]float64, len(arr))
	for _, t := range arr {
		sym, ok := symMap[t.Symbol]
		if !ok {
			continue
		}
		var p float64
		if _, err := fmt.Sscanf(t.LastPrice, "%f", &p); err != nil || p <= 0 {
			continue
		}
		out[sym] = p
	}
	return out, nil
}
