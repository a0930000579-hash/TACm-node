package exchange

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RefMarket M104：主流幣參考行情（估值對映——非鏈上資產，僅交易所內估值交易）。
// 與錢包「USDT 估值對映」同源：價格為伺服器端參考快照，鏈上資產不變。
type RefMarket struct {
	Symbol string  `json:"symbol"` // "BTC/USDT"
	Base   string  `json:"base"`   // "BTC"
	Price  float64 `json:"price"`  // 行情價（USDT）
	Chg24h float64 `json:"chg24h"` // 24h 漲跌幅（%）
	Live   bool    `json:"live"`   // true=即時外部行情；false=靜態參考
}

// refMarkets M104：主流幣參考行情表（可後續接外部行情源替換）。
var refMarkets = []RefMarket{
	{Symbol: "BTC/USDT", Base: "BTC", Price: 83923.68, Chg24h: 1.13},
	{Symbol: "ETH/USDT", Base: "ETH", Price: 2560.16, Chg24h: 2.09},
	{Symbol: "BNB/USDT", Base: "BNB", Price: 753.57, Chg24h: 0.77},
	{Symbol: "SOL/USDT", Base: "SOL", Price: 112.48, Chg24h: 2.62},
	{Symbol: "ADA/USDT", Base: "ADA", Price: 0.2532, Chg24h: 2.30},
}

// RefMarkets 返回主流幣行情清單（即時外部行情或靜態 fallback）。
func RefMarkets() []RefMarket {
	ensureLive()
	px, _, _ := liveSnapshot()
	out := make([]RefMarket, 0, len(refMarkets))
	for _, rm := range refMarkets {
		if lv, ok := px[rm.Symbol]; ok {
			out = append(out, lv)
		} else {
			out = append(out, rm)
		}
	}
	return out
}

// IsRefMarket 判斷是否為主流幣市場，回傳最新行情（即時優先）。
func IsRefMarket(s string) (RefMarket, bool) {
	u := strings.ToUpper(strings.TrimSpace(s))
	for _, rm := range refMarkets {
		if strings.ToUpper(rm.Symbol) != u {
			continue
		}
		if lv, ok := liveLookup(rm.Symbol); ok {
			return lv, true
		}
		return rm, true
	}
	return RefMarket{}, false
}

// liveLookup 即時行情查詢（無則 false）。
func liveLookup(sym string) (RefMarket, bool) {
	liveMu.Lock()
	defer liveMu.Unlock()
	if lv, ok := livePx[sym]; ok {
		return lv, true
	}
	return RefMarket{}, false
}

// IsRefBase 判斷資產是否為主流幣參考資產（BTC/ETH/BNB/SOL/ADA）。
func IsRefBase(a Asset) bool {
	for _, rm := range refMarkets {
		if Asset(rm.Base) == a {
			return true
		}
	}
	return false
}

// MarketQuote M104：市場行情卡（鏈上真實 + 主流幣參考）。
type MarketQuote struct {
	Market string  `json:"market"`
	Base   string  `json:"base"`
	Price  float64 `json:"price"`
	Chg24h float64 `json:"chg24h"`
	Ref    bool    `json:"ref"`
	Live   bool    `json:"live"`
	FeeBps int     `json:"fee_bps"`
}

// MarketsOverview M104：交易所首頁市場列表——鏈上三市場（真實價格＋24h 漲跌）
// ＋主流幣五市場（參考行情）。點選任一市場即可進入該幣種獨立交易頁。
func (s *Service) MarketsOverview() ([]MarketQuote, error) {
	out := []MarketQuote{}
	// 鏈上市場（真實撮合）。
	chainMkts := []Market{
		{AssetTACm, AssetUSDT},
		{AssetTACm, AssetTiUSD},
		{AssetTiUSD, AssetUSDT},
	}
	for _, m := range chainMkts {
		last, chg := s.marketLastChg(m.String())
		fr := m.FeeRates()
		out = append(out, MarketQuote{
			Market: m.String(), Base: string(m.Base),
			Price: last, Chg24h: chg, Ref: false,
			FeeBps: int(fr.TakerPpm / 100),
		})
	}
	// 主流幣市場（即時外部行情或靜態 fallback）。
	for _, rm := range RefMarkets() {
		out = append(out, MarketQuote{
			Market: rm.Symbol, Base: rm.Base,
			Price: rm.Price, Chg24h: rm.Chg24h, Ref: true, Live: rm.Live,
			FeeBps: 50, // 0.5%
		})
	}
	return out, nil
}

// marketLastChg 返回市場最新成交價與 24h 漲跌幅（klines 首尾計算；無數據時回參考值）。
func (s *Service) marketLastChg(market string) (float64, float64) {
	kl, err := s.Klines(market, 60, 24*60) // 1m x 1440 ≈ 24h
	if err != nil || len(kl) == 0 {
		return 0, 0
	}
	first := kl[0].Open
	last := kl[len(kl)-1].Close
	if first <= 0 {
		return last, 0
	}
	return last, (last - first) / first * 100
}

// RefKlines M104：主流幣參考市場 K 線——以參考價為錨的平滑歷史序列（僅展示用）。
// binKlines 幣安真實 K 線快取（30s TTL）。
var (
	binKlinesMu   sync.Mutex
	binKlinesData map[string][]Kline
	binKlinesAt   map[string]time.Time
	binFailAt     map[string]time.Time
)

// binanceKlines 拉取幣安真實 K 線（優先），失敗回傳錯誤由呼叫方 fallback。
func binanceKlines(market string, intervalSec, limit int) ([]Kline, error) {
	iv := map[int]string{60: "1m", 300: "5m", 900: "15m", 3600: "1h", 86400: "1d"}[intervalSec]
	if iv == "" {
		return nil, fmt.Errorf("exchange: 不支援週期")
	}
	binKlinesMu.Lock()
	k := market + ":" + iv
	if binKlinesData[k] != nil && time.Since(binKlinesAt[k]) < 30*time.Second {
		out := binKlinesData[k]
		binKlinesMu.Unlock()
		return out, nil
	}
	if time.Since(binFailAt[k]) < 30*time.Second {
		binKlinesMu.Unlock()
		return nil, fmt.Errorf("exchange: binance 暫不可用")
	}
	binKlinesMu.Unlock()
	sym := strings.Replace(market, "/", "", 1)
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(binanceKlinesURL(sym, iv, limit))
	if err != nil {
		binKlinesMu.Lock()
		if binFailAt == nil { binFailAt = map[string]time.Time{} }
		binFailAt[k] = time.Now()
		binKlinesMu.Unlock()
		return nil, err
	}
	defer resp.Body.Close()
	var raw [][]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil || len(raw) == 0 {
		binKlinesMu.Lock()
		if binFailAt == nil { binFailAt = map[string]time.Time{} }
		binFailAt[k] = time.Now()
		binKlinesMu.Unlock()
		return nil, fmt.Errorf("exchange: binance klines 解析失敗")
	}
	out := make([]Kline, 0, len(raw))
	for _, k := range raw {
		if len(k) < 6 {
			continue
		}
		// 幣安 klines 數值欄位是 JSON 字串（如 "0.01634790"），需字串→float 解析。
		var ts int64
		_ = json.Unmarshal(k[0], &ts)
		if ts == 0 {
			var tsS string
			if json.Unmarshal(k[0], &tsS) == nil {
				ts, _ = strconv.ParseInt(tsS, 10, 64)
			}
		}
		f := func(idx int) float64 {
			var sv string
			if json.Unmarshal(k[idx], &sv) == nil {
				if v, err := strconv.ParseFloat(sv, 64); err == nil {
					return v
				}
			}
			var n float64
			if json.Unmarshal(k[idx], &n) == nil {
				return n
			}
			return 0
		}
		o, h, l, c, v := f(1), f(2), f(3), f(4), f(5)
		out = append(out, Kline{Ts: ts / 1000, Open: round8(o), High: round8(h), Low: round8(l), Close: round8(c), Volume: round8(v)})
	}
	if len(out) == 0 {
		binKlinesMu.Lock()
		if binFailAt == nil { binFailAt = map[string]time.Time{} }
		binFailAt[k] = time.Now()
		binKlinesMu.Unlock()
		return nil, fmt.Errorf("exchange: binance klines 空")
	}
	binKlinesMu.Lock()
	if binKlinesData == nil {
		binKlinesData = map[string][]Kline{}
		binKlinesAt = map[string]time.Time{}
	}
	binKlinesData[k] = out
	binKlinesAt[k] = time.Now()
	binKlinesMu.Unlock()
	return out, nil
}

func (s *Service) RefKlines(market string, intervalSec, limit int) ([]Kline, error) {
	rm, ok := IsRefMarket(market)
	if !ok {
		return nil, fmt.Errorf("exchange: 非參考市場 %s", market)
	}
	if intervalSec <= 0 {
		intervalSec = 60
	}
	if limit <= 0 || limit > 500 {
		limit = 150
	}
	// 真實全球行情優先：幣安 K 線（30s 快取），失敗才回退本地模擬。
	if ks, err := binanceKlines(market, intervalSec, limit); err == nil && len(ks) > 0 {
		return ks, nil
	}
	// fallback：固定時間錨（anchored 到 interval 邊界）——避免每次輪詢時間軸滑動造成「亂跳」。
	base := rm.Price
	if base <= 0 || math.IsNaN(base) || math.IsInf(base, 0) {
		base = 1.0 // 防護：價格缺失時不產全 0 K 線
	}
	chg := rm.Chg24h
	if math.IsNaN(chg) || math.IsInf(chg, 0) {
		chg = 0
	}
	now := time.Now().Unix()
	now = now - now%int64(intervalSec)
	out := make([]Kline, 0, limit)
	for i := limit - 1; i >= 0; i-- {
		ts := now - int64(i)*int64(intervalSec)
		frac := float64(i) / float64(limit)
		phase := frac * 6.283
		drift := (frac - 0.5) * chg / 100 * 2
		mid := base * (1 + drift + 0.012*math.Sin(phase))
		// 紅綠交替：以相位決定當根漲跌方向，呈現幣安式紅綠蠟燭；相位加入 interval 因子讓不同週期走勢不同
		dir := math.Sin(phase*1.7 + 1.2 + float64(intervalSec)/86400.0*6.283*0.8)
		body := 0.012 * math.Abs(dir)
		var open, close float64
		if dir >= 0 {
			open = mid * (1 - body)
			close = mid * (1 + body)
		} else {
			open = mid * (1 + body)
			close = mid * (1 - body)
		}
		high := math.Max(open, close) * 1.003
		low := math.Min(open, close) * 0.997
		vol := 50 + 200*math.Abs(math.Sin(phase+1))
		out = append(out, Kline{Ts: ts, Open: round8(open), High: round8(high),
			Low: round8(low), Close: round8(close), Volume: round8(vol)})
	}
	return out, nil
}

// RefTrade M104：主流幣估值兌換——買：USDT → 主流幣（按參考價＋0.5% 費）；
// 賣：主流幣 → USDT（按參考價，收 0.5% 費）。餘額寫 ex_balances（asset=BTC 等）。
func (s *Service) RefTrade(uid, market, side string, qty float64) (Trade, error) {
	rm, ok := IsRefMarket(market)
	if !ok {
		return Trade{}, fmt.Errorf("exchange: 非參考市場 %s", market)
	}
	if uid == "" {
		return Trade{}, fmt.Errorf("exchange: 需要地址")
	}
	if qty <= 0 || math.IsNaN(qty) || math.IsInf(qty, 0) {
		return Trade{}, fmt.Errorf("exchange: 數量非法")
	}
	base := Asset(rm.Base)
	side = strings.ToLower(strings.TrimSpace(side))
	feePct := 0.005 // 0.5%
	switch side {
	case "buy":
		cost := qty * rm.Price
		fee := cost * feePct
		total := cost + fee
		qAmt, aerr := amtF(qty, base)
		if aerr != nil {
			return Trade{}, fmt.Errorf("exchange: 數量格式: %w", aerr)
		}
		uAmt, aerr := amtF(total, AssetUSDT)
		if aerr != nil {
			return Trade{}, fmt.Errorf("exchange: 金額格式: %w", aerr)
		}
		// M153 修復：先扣 USDT（餘額不足直接拒絕），成功後才入帳 base——避免「無資產也能成交」。
		if err := s.Withdraw(uid, AssetUSDT, uAmt); err != nil {
			return Trade{}, fmt.Errorf("exchange: 餘額不足（需 USDT %s）: %w", uAmt.String(AssetUSDT), err)
		}
		if err := s.Deposit(uid, base, qAmt, ""); err != nil {
			_ = s.Deposit(uid, AssetUSDT, uAmt, "") // 入帳失敗則回滾 USDT 扣款
			return Trade{}, fmt.Errorf("exchange: 兌換入帳失敗: %w", err)
		}
	case "sell":
		proceeds := qty * rm.Price
		fee := proceeds * feePct
		uAmt, aerr := amtF(proceeds-fee, AssetUSDT)
		if aerr != nil {
			return Trade{}, fmt.Errorf("exchange: 金額格式: %w", aerr)
		}
		qAmt, aerr := amtF(qty, base)
		if aerr != nil {
			return Trade{}, fmt.Errorf("exchange: 數量格式: %w", aerr)
		}
		// M153 修復：先扣 base（持倉不足直接拒絕），成功後才入帳 USDT——避免「無持倉也能賣出」。
		if err := s.Withdraw(uid, base, qAmt); err != nil {
			return Trade{}, fmt.Errorf("exchange: 持倉不足（需 %s %s）: %w", qAmt.String(base), base, err)
		}
		if err := s.Deposit(uid, AssetUSDT, uAmt, ""); err != nil {
			_ = s.Deposit(uid, base, qAmt, "") // 入帳失敗則回滾 base 扣款
			return Trade{}, fmt.Errorf("exchange: 兌換入帳失敗: %w", err)
		}
	default:
		return Trade{}, fmt.Errorf("exchange: side 需為 buy|sell")
	}
	ts := time.Now().Unix()
	return Trade{
		ID: ts * 1000, Market: market, Ts: ts,
		TakerSide: Side(side),
		Price:     mustAmt(rm.Price, AssetUSDT),
		Qty:       mustAmt(qty, base),
		Quote:     mustAmt(qty*rm.Price, AssetUSDT),
		PriceStr:  trimF(rm.Price),
		QtyStr:    trimF(qty),
		QuoteStr:  trimF(qty * rm.Price),
		TakerUID:  uid, TakerOrderID: 0, MakerOrderID: 0,
	}, nil
}

// RefPositions M104：主流幣估值持倉（BTC/ETH/BNB/SOL/ADA 餘額＋USDT 可用）。
func (s *Service) RefPositions(uid string) ([]*Balance, error) {
	if uid == "" {
		return []*Balance{}, nil
	}
	all, err := s.Balances(uid)
	if err != nil {
		return nil, err
	}
	out := []*Balance{}
	for _, b := range all {
		if IsRefBase(b.Asset) || b.Asset == AssetUSDT {
			out = append(out, b)
		}
	}
	return out, nil
}

// round8 四捨五入到 8 位小數。
func round8(f float64) float64 {
	return math.Round(f*1e8) / 1e8
}

// trimF 格式化浮點為無尾零字串。
func trimF(f float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.8f", f), "0"), ".")
}

// amtF 由浮點構造 Amount（精確到 8 位小數）。
func amtF(f float64, a Asset) (Amount, error) {
	return NewAmount(trimF(f), a)
}

// mustAmt 浮點構造 Amount（輸入已驗證，此處僅格式化，不應失敗；失敗回零值）。
func mustAmt(f float64, a Asset) Amount {
	am, _ := NewAmount(trimF(f), a)
	return am
}

// ---------------------------------------------------------------------------
// M104-3：即時真實行情（外部行情源）
// 節點啟動後首次存取市場時，從 CoinGecko（免費、免 key）拉取主流幣即時價格，
// 快取 60 秒；拉取失敗自動回退靜態參考表（refMarkets），不阻塞交易。
// 前端在市場列表顯示「即時」或「參考」來源標記。
// ---------------------------------------------------------------------------

// liveQuote 即時行情快照（單幣）。
type liveQuote struct {
	USD        float64 `json:"usd"`
	USD24hChg  float64 `json:"usd_24h_change"`
	USD24hChgSet bool  `json:"-"`
}

var (
	liveMu         sync.Mutex
	livePx         map[string]RefMarket // "BTC/USDT" -> 即時 RefMarket
	liveFetched    time.Time            // 上次成功拉取時間
	liveAttempt    time.Time            // 上次嘗試時間（防止過度重試）
	liveSource     string               // "coingecko" / "static"
	liveUpdated    time.Time
	liveRefreshing bool // M114：後台刷新中標記，避免併發重複拉取
)

// coingeckoCoinIDs CoinGecko coin id → 我們的市場符號。
var coingeckoCoinIDs = map[string]string{
	"bitcoin":     "BTC/USDT",
	"ethereum":    "ETH/USDT",
	"binancecoin": "BNB/USDT",
	"solana":      "SOL/USDT",
	"cardano":     "ADA/USDT",
}

var coingeckoURL = "https://api.coingecko.com/api/v3/simple/price?ids=bitcoin,ethereum,binancecoin,solana,cardano&vs_currencies=usd&include_24hr_change=true"

// binanceTickerURL 全球主流交易所（幣安）即時報價——24hr ticker。
var binanceTickerURL = "https://api.binance.com/api/v3/ticker/24hr?symbols=" +
	url.QueryEscape(`["BTCUSDT","ETHUSDT","BNBUSDT","SOLUSDT","ADAUSDT"]`)

// binanceKlinesURL 幣安真實 K 線（interval: 1m/5m/15m/1h/1d）。
func binanceKlinesURL(sym, iv string, limit int) string {
	return "https://api.binance.com/api/v3/klines?symbol=" + sym + "&interval=" + iv + "&limit=" + strconv.Itoa(limit)
}

// refreshLiveRef 拉取即時行情：優先 Binance（全球主流交易所即時對接），失敗回退 CoinGecko。
// 任何失敗皆靜默回退（沿用上次快取或靜態表），不阻塞交易。
func refreshLiveRef() {
	liveMu.Lock()
	tooSoon := time.Since(liveAttempt) < 5*time.Second
	if tooSoon || liveRefreshing {
		liveMu.Unlock()
		return
	}
	liveRefreshing = true
	liveMu.Unlock()
	defer func() {
		liveMu.Lock()
		liveRefreshing = false
		liveMu.Unlock()
	}()
	client := &http.Client{Timeout: 4 * time.Second}
	// 1) Binance 優先
	if px, ok := fetchBinanceTicker(client); ok {
		liveMu.Lock()
		liveAttempt = time.Now()
		livePx = px
		liveFetched = time.Now()
		liveSource = "binance"
		liveUpdated = time.Now()
		liveMu.Unlock()
		return
	}
	// 2) CoinGecko fallback
	resp, err := client.Get(coingeckoURL)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}
	var raw map[string]liveQuote
	if err := json.Unmarshal(body, &raw); err != nil {
		return
	}
	px := make(map[string]RefMarket, len(coingeckoCoinIDs))
	okCnt := 0
	for id, sym := range coingeckoCoinIDs {
		q, ok := raw[id]
		if !ok || q.USD <= 0 {
			continue
		}
		rm := RefMarket{Symbol: sym, Base: strings.Split(sym, "/")[0], Price: q.USD, Live: true}
		if q.USD24hChgSet || q.USD24hChg != 0 {
			rm.Chg24h = q.USD24hChg
		} else if base, ok2 := staticRef(sym); ok2 {
			rm.Chg24h = base.Chg24h
		}
		px[sym] = rm
		okCnt++
	}
	liveMu.Lock()
	liveAttempt = time.Now()
	if okCnt > 0 {
		livePx = px
		liveFetched = time.Now()
		liveSource = "coingecko"
		liveUpdated = time.Now()
	}
	liveMu.Unlock()
}

// fetchBinanceTicker 呼叫幣安 24hr ticker，回傳主流幣即時報價。
func fetchBinanceTicker(client *http.Client) (map[string]RefMarket, bool) {
	resp, err := client.Get(binanceTickerURL)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	var arr []struct {
		Symbol             string `json:"symbol"`
		LastPrice          string `json:"lastPrice"`
		PriceChangePercent string `json:"priceChangePercent"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&arr); err != nil {
		return nil, false
	}
	px := make(map[string]RefMarket, len(arr))
	okCnt := 0
	for _, t := range arr {
		sym := binanceSymbolToMarket(t.Symbol)
		if sym == "" {
			continue
		}
		price, err1 := strconv.ParseFloat(t.LastPrice, 64)
		chg, err2 := strconv.ParseFloat(t.PriceChangePercent, 64)
		if err1 != nil || price <= 0 {
			continue
		}
		rm := RefMarket{Symbol: sym, Base: strings.Split(sym, "/")[0], Price: price, Live: true}
		if err2 == nil {
			rm.Chg24h = chg
		} else if base, ok2 := staticRef(sym); ok2 {
			rm.Chg24h = base.Chg24h
		}
		px[sym] = rm
		okCnt++
	}
	return px, okCnt > 0
}

// binanceSymbolToMarket 幣安符號 → 我們的市場符號。
func binanceSymbolToMarket(s string) string {
	m := map[string]string{
		"BTCUSDT": "BTC/USDT", "ETHUSDT": "ETH/USDT", "BNBUSDT": "BNB/USDT",
		"SOLUSDT": "SOL/USDT", "ADAUSDT": "ADA/USDT",
	}
	return m[s]
}

// staticRef 靜態表查詢（fallback 底價）。
func staticRef(sym string) (RefMarket, bool) {
	for _, rm := range refMarkets {
		if rm.Symbol == sym {
			return rm, true
		}
	}
	return RefMarket{}, false
}

// liveSnapshot 返回當前活躍行情表（即時或靜態 fallback）＋來源資訊。
func liveSnapshot() (map[string]RefMarket, string, time.Time) {
	liveMu.Lock()
	defer liveMu.Unlock()
	if len(livePx) > 0 {
		return livePx, liveSource, liveUpdated
	}
	// fallback 靜態表
	px := make(map[string]RefMarket, len(refMarkets))
	for _, rm := range refMarkets {
		px[rm.Symbol] = rm
	}
	return px, "static", time.Time{}
}

// ensureLive 確保行情新鮮（60s TTL），過期則同步拉取一次。
func ensureLive() {
	liveMu.Lock()
	stale := len(livePx) == 0 || time.Since(liveFetched) > 15*time.Second
	liveMu.Unlock()
	if stale {
		// M114：後台異步刷新——行情來源（幣安/CoinGecko）慢或掛時不阻塞讀取，
		// 交易所 WebSocket 推送/輪詢即時返回（沿用快取或靜態價），新價格到達後自動生效。
		go refreshLiveRef()
	}
}

// LiveSource 返回目前行情來源與更新時間（供 RPC/前端標記）。
func LiveSource() (string, time.Time) {
	liveMu.Lock()
	defer liveMu.Unlock()
	if len(livePx) > 0 {
		return liveSource, liveUpdated
	}
	return "static", time.Time{}
}
