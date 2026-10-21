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

// TestRefTradeRequiresBalance M153：驗證「無資產不可交易」——buy 需 USDT、sell 需 base 持倉。
func TestRefTradeRequiresBalance(t *testing.T) {
	svc := newSvc(t)
	const uid = "tx0test1111111111111111111111111111"

	// 1) 完全無資產：buy 應拒絕（餘額不足），且不得入帳任何 base。
	if _, err := svc.RefTrade(uid, "BTC/USDT", "buy", 0.01); err == nil {
		t.Fatal("無 USDT 買 BTC 應被拒絕")
	}
	// 關鍵：拒絕後 base 不得入帳（舊 bug：先入帳後扣款，白嫖 BTC）。
	if b, _ := svc.Balances(uid); len(b) != 0 {
		t.Fatalf("拒絕後不應有任何餘額，got %+v", b)
	}

	// 2) 完全無持倉：sell 應拒絕。
	if _, err := svc.RefTrade(uid, "ETH/USDT", "sell", 0.1); err == nil {
		t.Fatal("無 ETH 賣出應被拒絕")
	}

	// 3) 入 USDT 後 buy：成功，USDT 扣、BTC 入。
	if err := svc.Deposit(uid, AssetUSDT, exAmt("10000", AssetUSDT), "seed"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RefTrade(uid, "BTC/USDT", "buy", 0.01); err != nil {
		t.Fatalf("有 USDT 買 BTC 應成功: %v", err)
	}
	bal, _ := svc.Balances(uid)
	gotUSDT, gotBTC := false, false
	for _, b := range bal {
		if b.Asset == AssetUSDT && b.Avail.Cmp(exAmt("10000", AssetUSDT)) < 0 {
			gotUSDT = true // USDT 已扣款
		}
		if b.Asset == Asset("BTC") && b.Avail.Cmp(Amount{}) > 0 {
			gotBTC = true // BTC 已入帳
		}
	}
	if !gotUSDT || !gotBTC {
		t.Fatalf("buy 成功後應扣 USDT 入 BTC，got %+v", bal)
	}

	// 4) 有 BTC 後 sell：成功。
	if _, err := svc.RefTrade(uid, "BTC/USDT", "sell", 0.005); err != nil {
		t.Fatalf("有持倉賣出應成功: %v", err)
	}
}
