// Package oracle M152：節點級鏈上預言機（Oracle）。
// 複用 internal/exchange 的主流幣參考行情源（幣安/CoinGecko 即時 + 靜態 fallback），
// 提供統一喂價快照、單價查詢、手動覆寫與歷史紀錄，供 DeFi/開發者取用。
// 以 Go 慣用寫法實作：goroutine 定時刷新、sync.RWMutex 併發保護、完整 error handling。
package oracle

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"time"

	"tacm/internal/exchange"
)

// Price 為一筆喂價快照。
type Price struct {
	Symbol    string  `json:"symbol"`     // 市場符號，如 "BTC/USDT"
	Base      string  `json:"base"`       // 基礎資產，如 "BTC"
	Price     float64 `json:"price"`      // 報價（以 USDT 計）
	Chg24h    float64 `json:"chg24h"`     // 24h 漲跌幅（%）
	Source    string  `json:"source"`     // live | static | manual
	Live      bool    `json:"live"`       // true=即時外部行情
	UpdatedAt int64   `json:"updated_at"` // Unix 秒（UTC）
}

// Oracle 為節點級喂價服務（M153 主流規格化：多 feeder 提交 → 中位數聚合 → round/新鮮度）。
type Oracle struct {
	mu      sync.RWMutex
	cache   map[string]Price
	history map[string][]Price
	maxHist int
	manual  map[string]float64 // 手動覆寫價（符號 → 價格）
	started time.Time

	// M153：去中心化喂價聚合。
	feederPool map[string][]FeederSubmission // 本輪各 feeder 提交（symbol → submissions）
	round      map[string]uint64             // 各 symbol 當前聚合 round
	staleAfter int64                         // 新鮮度閾值（秒）
	minFeeders int                           // 觸發中位數聚合所需最少 feeder 數
}

// FeederSubmission 單一 feeder 的一筆喂價提交。
type FeederSubmission struct {
	Feeder string  `json:"feeder"`
	Price  float64 `json:"price"`
	Ts     int64   `json:"ts"`
}

// New 建立 Oracle 服務。
func New() *Oracle {
	return &Oracle{
		cache:      make(map[string]Price),
		history:    make(map[string][]Price),
		maxHist:    200,
		manual:     make(map[string]float64),
		started:    time.Now(),
		feederPool: make(map[string][]FeederSubmission),
		round:      make(map[string]uint64),
		staleAfter: 300, // 5 分鐘未更新視為 stale（Chainlink 心跳等級）
		minFeeders: 3,   // 至少 3 個獨立 feeder 才觸發中位數聚合
	}
}

// Snapshot 刷新並回傳全部喂價（手動覆寫優先於外部行情）。
func (o *Oracle) Snapshot() []Price {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.refreshLocked()
}

// refreshLocked 取外部行情並與手動覆寫合併（呼叫端須持鎖）。
func (o *Oracle) refreshLocked() []Price {
	markets := exchange.RefMarkets()
	now := time.Now().Unix()
	out := make([]Price, 0, len(markets)+len(o.manual))
	for _, rm := range markets {
		sym := strings.ToUpper(rm.Symbol)
		src := "static"
		if rm.Live {
			src = "live"
		}
		p := Price{Symbol: sym, Base: rm.Base, Price: rm.Price, Chg24h: rm.Chg24h,
			Source: src, Live: rm.Live, UpdatedAt: now}
		if mv, ok := o.manual[sym]; ok {
			p.Price = mv
			p.Source = "manual"
			p.Live = true
		}
		o.cache[sym] = p
		out = append(out, p)
	}
	for sym, mv := range o.manual {
		if _, known := o.cache[sym]; known {
			continue
		}
		p := Price{Symbol: sym, Base: strings.Split(sym, "/")[0], Price: mv,
			Source: "manual", Live: true, UpdatedAt: now}
		o.cache[sym] = p
		out = append(out, p)
	}
	return out
}

// Get 查詢單一市場喂價。
func (o *Oracle) Get(symbol string) (Price, bool) {
	sym := strings.ToUpper(strings.TrimSpace(symbol))
	o.mu.Lock()
	defer o.mu.Unlock()
	// 未在快取則嘗試即時查詢（手動覆寫優先）。
	if rm, ok := exchange.IsRefMarket(sym); ok {
		now := time.Now().Unix()
		src := "static"
		if rm.Live {
			src = "live"
		}
		p := Price{Symbol: strings.ToUpper(rm.Symbol), Base: rm.Base, Price: rm.Price,
			Chg24h: rm.Chg24h, Source: src, Live: rm.Live, UpdatedAt: now}
		if mv, ok := o.manual[sym]; ok {
			p.Price = mv
			p.Source = "manual"
			p.Live = true
		}
		o.cache[sym] = p
		return p, true
	}
	if p, ok := o.cache[sym]; ok {
		return p, true
	}
	return Price{}, false
}

// Feed 手動覆寫喂價（管理員/節點操作，價格須為正有限值）。
func (o *Oracle) Feed(symbol string, price float64) error {
	sym := strings.ToUpper(strings.TrimSpace(symbol))
	if sym == "" {
		return errors.New("oracle: symbol required")
	}
	if math.IsNaN(price) || math.IsInf(price, 0) || price <= 0 {
		return errors.New("oracle: price must be a positive finite number")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.manual[sym] = price
	// 立即反映到快取。
	now := time.Now().Unix()
	base := sym
	if i := strings.Index(sym, "/"); i > 0 {
		base = sym[:i]
	}
	p := Price{Symbol: sym, Base: base, Price: price, Source: "manual", Live: true, UpdatedAt: now}
	if old, ok := o.cache[sym]; ok {
		p.Chg24h = old.Chg24h
	}
	o.cache[sym] = p
	o.appendHistoryLocked(p)
	return nil
}

// History 回傳指定市場的歷史喂價（新→舊）。
func (o *Oracle) History(symbol string) []Price {
	sym := strings.ToUpper(strings.TrimSpace(symbol))
	o.mu.RLock()
	defer o.mu.RUnlock()
	h, ok := o.history[sym]
	if !ok {
		return nil
	}
	out := make([]Price, len(h))
	copy(out, h)
	return out
}

// SubmitFeeder M153：feeder（獨立節點/資料源）提交一筆喂價到本輪聚合池。
func (o *Oracle) SubmitFeeder(feeder, symbol string, price float64) error {
	sym := strings.ToUpper(strings.TrimSpace(symbol))
	feeder = strings.TrimSpace(feeder)
	if sym == "" || feeder == "" {
		return errors.New("oracle: feeder and symbol required")
	}
	if math.IsNaN(price) || math.IsInf(price, 0) || price <= 0 {
		return errors.New("oracle: price must be a positive finite number")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.feederPool[sym] = append(o.feederPool[sym], FeederSubmission{
		Feeder: feeder, Price: price, Ts: time.Now().Unix(),
	})
	return nil
}

// AggregateRound M153：聚合本輪 feeder 提交。
// 規則（Chainlink 風）：某 symbol 收集到 ≥ minFeeders 個獨立 feeder 提交時，
// 取中位數作為正式價格（round++，source="aggregate"）；不足則沿用外部行情。
func (o *Oracle) AggregateRound() []Price {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := time.Now().Unix()
	out := o.refreshLocked()
	for sym, subs := range o.feederPool {
		if len(subs) < o.minFeeders {
			continue // feeder 不足，沿用外部行情（已在 refreshLocked 寫入 cache）
		}
		prices := make([]float64, 0, len(subs))
		for _, s := range subs {
			prices = append(prices, s.Price)
		}
		sortFloats(prices)
		median := prices[len(prices)/2]
		base := sym
		if i := strings.Index(sym, "/"); i > 0 {
			base = sym[:i]
		}
		old := o.cache[sym]
		p := Price{Symbol: sym, Base: base, Price: median, Chg24h: old.Chg24h,
			Source: "aggregate", Live: true, UpdatedAt: now}
		o.cache[sym] = p
		o.round[sym]++
		o.appendHistoryLocked(p)
		// 更新 out 中對應項；若 refreshLocked 未涵蓋此 symbol（自訂餵價），則附加。
		found := false
		for i := range out {
			if out[i].Symbol == sym {
				out[i] = p
				found = true
			}
		}
		if !found {
			out = append(out, p)
		}
	}
	// 清空本輪提交池，準備下一輪。
	o.feederPool = make(map[string][]FeederSubmission)
	return out
}

// Stale M153：回傳某 symbol 喂價是否新鮮度過期（超過 staleAfter 秒未更新）。
func (o *Oracle) Stale(symbol string) bool {
	sym := strings.ToUpper(strings.TrimSpace(symbol))
	o.mu.RLock()
	defer o.mu.RUnlock()
	p, ok := o.cache[sym]
	if !ok {
		return true
	}
	return time.Now().Unix()-p.UpdatedAt > o.staleAfter
}

// Round 回傳某 symbol 當前聚合 round。
func (o *Oracle) Round(symbol string) uint64 {
	sym := strings.ToUpper(strings.TrimSpace(symbol))
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.round[sym]
}

// sortFloats 對 float64 切片升冪排序（簡單插入，feeder 數量小）。
func sortFloats(a []float64) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}

// appendHistoryLocked 追加歷史並限長（呼叫端須持寫鎖）。
func (o *Oracle) appendHistoryLocked(p Price) {
	h := o.history[p.Symbol]
	h = append([]Price{p}, h...)
	if len(h) > o.maxHist {
		h = h[:o.maxHist]
	}
	o.history[p.Symbol] = h
}

// Uptime 回傳服務已執行秒數。
func (o *Oracle) Uptime() int64 {
	return int64(time.Since(o.started).Seconds())
}

// Start 啟動背景喂價刷新 goroutine（context 取消即停止）。
func (o *Oracle) Start(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				o.mu.Lock()
				prices := o.refreshLocked()
				for _, p := range prices {
					o.appendHistoryLocked(p)
				}
				o.mu.Unlock()
			}
		}
	}()
}
