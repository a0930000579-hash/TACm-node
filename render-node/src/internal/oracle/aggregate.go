// Package oracle：去中心化多源聚合引擎（median-first aggregation）。
//
// 職責：
//   - 對每個 symbol 並行從多個 Source 拉報價；
//   - 中位數為主聚合價，加權平均為輔；
//   - 偏離中位數超過 band 的源標 outlier 並剔除；
//   - 單源逾時/錯誤 → 標 stale/error，用其餘源聚合，不panic、不歸零；
//   - 全部源失效 → 保留最後有效價並標 degraded/stale；
//   - 最小更新間隔節流（heartbeat），避免頻繁拉取。
//
// 公開型別 AggregateResult / SourceStatus 供 RPC（/api/oracle/sources、
// /api/oracle/aggregate）與前端 oracle.html 查詢。
package oracle

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// SourceStatus 單一數據源的最新狀態（RPC/前端展示用）。
type SourceStatus struct {
	Name      string  `json:"name"`            // 源名
	Weight    float64 `json:"weight"`          // 加權權重
	Price     float64 `json:"price"`           // 本源最新報價（無則 0）
	Adopted   bool    `json:"adopted"`         // 最近一輪是否被採用
	Stale     bool    `json:"stale"`           // 逾時/錯誤/過期
	Outlier   bool    `json:"outlier"`         // 偏離中位數被剔除
	UpdatedAt int64   `json:"updated_at"`      // 上次成功拉取（Unix 秒）
	Error     string  `json:"error,omitempty"` // 最近錯誤
}

// AggregateResult 單一 symbol 的多源聚合詳情。
type AggregateResult struct {
	Symbol     string         `json:"symbol"`
	Price      float64        `json:"price"`          // 最終採用價（中位數為主）
	Median     float64        `json:"median"`         // 中位數
	Weighted   float64        `json:"weighted_price"` // 加權平均（輔助參考）
	Confidence float64        `json:"confidence"`     // 0..1 置信度
	AdoptedN   int            `json:"adopted_count"`  // 實際參與聚合的源數
	Sources    []SourceStatus `json:"sources"`        // 各源狀態
	Stale      bool           `json:"stale"`          // 新鮮度過期
	Degraded   bool           `json:"degraded"`       // 降級（沿用最後有效價）
	Jump       bool           `json:"jump"`           // 突變標記（相較上一輪跳動過大）
	UpdatedAt  int64          `json:"updated_at"`
}

// srcRT 單源執行期狀態（引擎內部，不保 JSON）。
type srcRT struct {
	name     string
	weight   float64
	lastErr  string
	lastOkAt time.Time
	adopted  bool
	outlier  bool
	stale    bool
	quotes   map[string]float64 // sym → 最近一次成功報價
}

// aggEngine 多源聚合引擎（獨立 mutex，不與 Oracle.cache 互鎖）。
type aggEngine struct {
	mu          sync.RWMutex
	sources     []Source
	symbols     []string
	band        float64       // outlier 偏離分率（如 0.10＝±10%）
	jumpBand    float64       // 突變閾值
	staleAfter  time.Duration // 超過此時長未成功視為 stale
	minInterval time.Duration // 節流：最小刷新間隔
	lastRefresh time.Time
	results     map[string]AggregateResult
	rt          map[string]*srcRT // srcName → runtime
}

// defaultSymbols 預設聚合的主流幣市場。
var defaultSymbols = []string{"BTC/USDT", "ETH/USDT", "BNB/USDT", "SOL/USDT", "ADA/USDT"}

// newDefaultAggEngine 以合理預設組裝多源聚合引擎（New() 內呼叫）。
// 預設：exchange 參考源（主）＋ 靜態錨源（獨立第二源）＋ 可配置幣安 HTTP 源（第三源，失敗自降級）。
func newDefaultAggEngine() *aggEngine {
	staticPx := map[string]float64{
		"BTC/USDT": 83923.68, "ETH/USDT": 2560.16, "BNB/USDT": 753.57,
		"SOL/USDT": 112.48, "ADA/USDT": 0.2532,
	}
	e := &aggEngine{
		band:        0.10,
		jumpBand:    0.15,
		staleAfter:  90 * time.Second,
		minInterval: 30 * time.Second,
		results:     map[string]AggregateResult{},
		rt:          map[string]*srcRT{},
		symbols:     append([]string(nil), defaultSymbols...),
	}
	e.setSources([]Source{
		&RefSource{NameV: "exchange_ref", WeightV: 1.0},
		&StaticSource{NameV: "static_anchor", WeightV: 0.5, Px: staticPx},
		&HTTPSource{
			NameV:   "binance_http",
			WeightV: 1.0,
			URL: "https://api.binance.com/api/v3/ticker/24hr?symbols=" +
				`["BTCUSDT","ETHUSDT","BNBUSDT","SOLUSDT","ADAUSDT"]`,
		},
	})
	return e
}

// setSources 注入數據源（測試替換用；清空舊執行期狀態）。
func (e *aggEngine) setSources(sources []Source) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sources = append([]Source(nil), sources...)
	e.rt = map[string]*srcRT{}
	for _, s := range e.sources {
		e.rt[s.Name()] = &srcRT{name: s.Name(), weight: s.Weight(), quotes: map[string]float64{}}
	}
}

// setBand 設定 outlier 偏離閾值（分率）。
func (e *aggEngine) setBand(b float64) { e.mu.Lock(); e.band = b; e.mu.Unlock() }

// setMinInterval 設定節流最小刷新間隔。
func (e *aggEngine) setMinInterval(d time.Duration) { e.mu.Lock(); e.minInterval = d; e.mu.Unlock() }

// setSymbols 設定聚合 symbol 集合（測試用）。
func (e *aggEngine) setSymbols(syms []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.symbols = append([]string(nil), syms...)
}

// quoteBag 並行抓取期間的收集器。
type quoteBag struct {
	mu     sync.Mutex
	quotes map[string]map[string]float64 // srcName → sym → price
	errs   map[string]string
}

// Refresh 執行一輪多源聚合。force=true 略過節流（測試/手動觸發用）。
func (e *aggEngine) Refresh(ctx context.Context, force bool) {
	e.mu.RLock()
	band := e.band
	jumpBand := e.jumpBand
	staleAfter := e.staleAfter
	minInterval := e.minInterval
	lastRefresh := e.lastRefresh
	sources := append([]Source(nil), e.sources...)
	symbols := append([]string(nil), e.symbols...)
	e.mu.RUnlock()

	if !force && minInterval > 0 && !lastRefresh.IsZero() && time.Since(lastRefresh) < minInterval {
		return // 節流命中：本輪跳過，沿用上次結果
	}
	if len(sources) == 0 || len(symbols) == 0 {
		return
	}

	// 並行抓取各源（單源失敗只記錯，不影響其他源）。
	bag := &quoteBag{quotes: map[string]map[string]float64{}, errs: map[string]string{}}
	var wg sync.WaitGroup
	for _, s := range sources {
		wg.Add(1)
		go func(s Source) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
			defer cancel()
			px, err := s.Fetch(cctx, symbols)
			bag.mu.Lock()
			defer bag.mu.Unlock()
			if err != nil {
				bag.errs[s.Name()] = err.Error()
			} else {
				bag.quotes[s.Name()] = px
			}
		}(s)
	}
	wg.Wait()

	e.applyBag(band, jumpBand, staleAfter, sources, symbols, bag)
}

// applyBag 把本輪抓取結果寫入聚合結果（持寫鎖）。
func (e *aggEngine) applyBag(band, jumpBand float64, staleAfter time.Duration,
	sources []Source, symbols []string, bag *quoteBag) {

	now := time.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lastRefresh = now

	// 先更新每源執行期狀態（跨 symbol 彙總）。
	for _, s := range sources {
		rt, ok := e.rt[s.Name()]
		if !ok {
			rt = &srcRT{name: s.Name(), weight: s.Weight(), quotes: map[string]float64{}}
			e.rt[s.Name()] = rt
		}
		rt.weight = s.Weight()
		rt.adopted = false
		rt.outlier = false
		if errStr, hasErr := bag.errs[s.Name()]; hasErr {
			rt.lastErr = errStr
			rt.stale = true
		} else {
			rt.lastErr = ""
			rt.lastOkAt = now
			if px, ok := bag.quotes[s.Name()]; ok {
				for sym, p := range px {
					rt.quotes[sym] = p
				}
			}
			rt.stale = false
		}
	}

	weights := map[string]float64{}
	for _, s := range sources {
		weights[s.Name()] = s.Weight()
	}

	for _, sym := range symbols {
		// 收集本源在這個 symbol 的觀測。
		var all []obs
		total := 0
		for _, s := range sources {
			total++
			px, ok := bag.quotes[s.Name()]
			if !ok {
				continue
			}
			if p, ok := px[sym]; ok && p > 0 {
				all = append(all, obs{Name: s.Name(), Price: p})
			}
		}

		srcStatus := make([]SourceStatus, 0, len(sources))
		if len(all) == 0 {
			// 全部源本輪失敗：降級，保留最後有效價。
			prev := e.results[sym]
			for _, s := range sources {
				rt := e.rt[s.Name()]
				srcStatus = append(srcStatus, SourceStatus{
					Name: s.Name(), Weight: s.Weight(),
					Adopted: false, Stale: true, Outlier: false,
					UpdatedAt: rt.lastOkAt.Unix(), Error: rt.lastErr,
				})
			}
			e.results[sym] = AggregateResult{
				Symbol: sym, Price: prev.Price, Median: prev.Median, Weighted: prev.Weighted,
				Confidence: 0, AdoptedN: 0, Sources: srcStatus,
				Stale: true, Degraded: true, Jump: prev.Jump, UpdatedAt: now.Unix(),
			}
			continue
		}

		st := classifyObservations(all, band, weights)
		prev := e.results[sym]
		jump := jumpDetected(prev.Median, st.Median, jumpBand)

		adoptedSet := map[string]struct{}{}
		adoptedPxs := make([]float64, 0, len(st.Adopted))
		for _, o := range st.Adopted {
			adoptedSet[o.Name] = struct{}{}
			adoptedPxs = append(adoptedPxs, o.Price)
			if rt, ok := e.rt[o.Name]; ok {
				rt.adopted = true
			}
		}
		outlierSet := map[string]struct{}{}
		for _, o := range st.Outliers {
			outlierSet[o.Name] = struct{}{}
			if rt, ok := e.rt[o.Name]; ok {
				rt.outlier = true
			}
		}

		conf := confidence(len(st.Adopted), total, adoptedPxs)
		for _, s := range sources {
			rt := e.rt[s.Name()]
			_, adopted := adoptedSet[s.Name()]
			_, isOut := outlierSet[s.Name()]
			_, hasErr := bag.errs[s.Name()]
			stale := hasErr || time.Since(rt.lastOkAt) > staleAfter
			price := 0.0
			if px, ok := bag.quotes[s.Name()]; ok {
				price = px[sym]
			} else if p, ok := rt.quotes[sym]; ok {
				price = p
			}
			srcStatus = append(srcStatus, SourceStatus{
				Name: s.Name(), Weight: s.Weight(), Price: price,
				Adopted: adopted, Stale: stale, Outlier: isOut,
				UpdatedAt: rt.lastOkAt.Unix(), Error: bag.errs[s.Name()],
			})
		}
		sort.Slice(srcStatus, func(i, j int) bool { return srcStatus[i].Name < srcStatus[j].Name })

		e.results[sym] = AggregateResult{
			Symbol: sym, Price: st.Median, Median: st.Median, Weighted: st.Weighted,
			Confidence: conf, AdoptedN: len(st.Adopted), Sources: srcStatus,
			Stale: false, Degraded: false, Jump: jump, UpdatedAt: now.Unix(),
		}
	}
}

// Get 回傳某 symbol 最近一次聚合結果（未聚過則 false）。
func (e *aggEngine) Get(sym string) (AggregateResult, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	r, ok := e.results[strings.ToUpper(strings.TrimSpace(sym))]
	return r, ok
}

// SourcesStatus 回傳各源跨 symbol 的最新狀態（RPC /sources 用）。
func (e *aggEngine) SourcesStatus() []SourceStatus {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]SourceStatus, 0, len(e.rt))
	for _, rt := range e.rt {
		// 跨 symbol 取最近一筆有報價的價格作展示。
		price := 0.0
		for _, p := range rt.quotes {
			if p > 0 {
				price = p
				break
			}
		}
		out = append(out, SourceStatus{
			Name: rt.name, Weight: rt.weight, Price: price,
			Adopted: rt.adopted, Stale: rt.stale, Outlier: rt.outlier,
			UpdatedAt: rt.lastOkAt.Unix(), Error: rt.lastErr,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ===========================================================================
// Oracle 公開委派方法（向後相容：不改 New() 簽名、不改 Get 回傳型別）
// ===========================================================================

// SetAggSources 注入多源數據源（測試替身 / 節點擴展用）。
// 預設 New() 已組裝 exchange_ref + static_anchor + binance_http。
func (o *Oracle) SetAggSources(sources ...Source) {
	if o.agg != nil {
		o.agg.setSources(sources)
	}
}

// SetAggSymbols 設定聚合 symbol 集合（測試用）。
func (o *Oracle) SetAggSymbols(syms []string) {
	if o.agg != nil {
		o.agg.setSymbols(syms)
	}
}

// SetAggBand 設定 outlier 偏離閾值（分率，如 0.10＝±10%）。
func (o *Oracle) SetAggBand(band float64) {
	if o.agg != nil {
		o.agg.setBand(band)
	}
}

// SetAggMinInterval 設定聚合最小刷新間隔（節流 heartbeat）。
func (o *Oracle) SetAggMinInterval(d time.Duration) {
	if o.agg != nil {
		o.agg.setMinInterval(d)
	}
}

// RefreshAggregate 觸發一輪多源聚合（受節流保護；背景 Start ticker 會呼叫）。
func (o *Oracle) RefreshAggregate(ctx context.Context) {
	if o.agg != nil {
		o.agg.Refresh(ctx, false)
	}
}

// ForceRefreshAggregate 略過節流強制刷新（RPC/測試用）。
func (o *Oracle) ForceRefreshAggregate(ctx context.Context) {
	if o.agg != nil {
		o.agg.Refresh(ctx, true)
	}
}

// SourcesStatus 回傳各源狀態（RPC GET /api/oracle/sources 用）。
func (o *Oracle) SourcesStatus() []SourceStatus {
	if o.agg == nil {
		return nil
	}
	return o.agg.SourcesStatus()
}

// AggregateDetail 回傳單一 symbol 的聚合詳情（RPC GET /api/oracle/aggregate?sym= 用）。
func (o *Oracle) AggregateDetail(sym string) (AggregateResult, bool) {
	if o.agg == nil {
		return AggregateResult{}, false
	}
	return o.agg.Get(sym)
}
