package exchange

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// TestRefreshLiveRef 驗證即時行情拉取：mock CoinGecko → live 表＋來源/時間。
func TestRefreshLiveRef(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"bitcoin":{"usd":84500.5,"usd_24h_change":2.1},"ethereum":{"usd":2561.9,"usd_24h_change":1.5},"binancecoin":{"usd":760.2,"usd_24h_change":0.9},"solana":{"usd":115.3,"usd_24h_change":3.2},"cardano":{"usd":0.261,"usd_24h_change":1.1}}`))
	}))
	defer srv.Close()
	old := coingeckoURL
	coingeckoURL = srv.URL
	defer func() { coingeckoURL = old }()

	refreshLiveRef()
	px, src, upd := liveSnapshot()
	if src != "coingecko" || upd.IsZero() {
		t.Fatalf("source=%s updated-zero=%v", src, upd.IsZero())
	}
	if len(px) != 5 {
		t.Fatalf("want 5 live markets, got %d", len(px))
	}
	btc, ok := px["BTC/USDT"]
	if !ok || btc.Price != 84500.5 || !btc.Live || btc.Chg24h != 2.1 {
		t.Fatalf("BTC live quote mismatch: %+v", btc)
	}
	// RefMarkets 應回傳即時價格
	mk := RefMarkets()
	for _, m := range mk {
		if m.Symbol == "BTC/USDT" && m.Price != 84500.5 {
			t.Fatalf("RefMarkets BTC price %v, want 84500.5", m.Price)
		}
	}
	// IsRefMarket 應回傳即時價
	rm, ok2 := IsRefMarket("btc/usdt")
	if !ok2 || rm.Price != 84500.5 || !rm.Live {
		t.Fatalf("IsRefMarket live mismatch: %+v", rm)
	}
}

// TestLiveFallback 驗證拉取失敗（mock 回 500）→ 靜態 fallback，不崩潰。
func TestLiveFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	old := coingeckoURL
	coingeckoURL = srv.URL
	defer func() { coingeckoURL = old }()
	// 清空 live 快取，確保走 fallback
	liveMu.Lock()
	livePx = nil
	liveFetched = time.Time{}
	liveMu.Unlock()
	mk := RefMarkets()
	if len(mk) != 5 {
		t.Fatalf("fallback markets=%d, want 5", len(mk))
	}
	if mk[0].Price <= 0 {
		t.Fatalf("fallback price invalid: %v", mk[0].Price)
	}
}


func TestBinanceKlinesStringParse(t *testing.T) {
	// 幣安 klines 數值欄位為 JSON 字串——必須正確解析而非全 0
	raw := `[["1791790000000","0.01634790","0.80000000","0.01575800","0.01577100","148976.11427815"]]`
	var arr [][]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &arr); err != nil || len(arr) != 1 {
		t.Fatalf("mock parse failed: %v", err)
	}
	k := arr[0]
	var ts int64
	_ = json.Unmarshal(k[0], &ts)
	if ts == 0 {
		var tsS string
		_ = json.Unmarshal(k[0], &tsS)
		ts, _ = strconv.ParseInt(tsS, 10, 64)
	}
	f := func(idx int) float64 {
		var sv string
		if json.Unmarshal(k[idx], &sv) == nil {
			if v, err := strconv.ParseFloat(sv, 64); err == nil {
				return v
			}
		}
		var n float64
		_ = json.Unmarshal(k[idx], &n)
		return n
	}
	o, h, l, c, v := f(1), f(2), f(3), f(4), f(5)
	if o != 0.01634790 || h != 0.8 || l != 0.01575800 || c != 0.01577100 || v != 148976.11427815 {
		t.Fatalf("string kline parse wrong: o=%v h=%v l=%v c=%v v=%v", o, h, l, c, v)
	}
	if ts != 1791790000000 {
		t.Fatalf("ts wrong: %v", ts)
	}
}

func TestRefKlinesNoZeroBase(t *testing.T) {
	// Price=0 時模擬 K 線不得全 0（NaN/異常防護）
	s := NewService(nil)
	ks, err := s.RefKlines("BTC/USDT", 60, 10)
	if err != nil {
		t.Fatalf("RefKlines err: %v", err)
	}
	if len(ks) == 0 {
		t.Fatal("no klines")
	}
	for _, k := range ks {
		if k.Open == 0 && k.High == 0 && k.Low == 0 && k.Close == 0 {
			t.Fatalf("all-zero kline at ts=%v", k.Ts)
		}
		if k.High < k.Low || k.High <= 0 {
			t.Fatalf("bad kline high/low: %+v", k)
		}
	}
}
