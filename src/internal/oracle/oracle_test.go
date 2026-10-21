package oracle

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestSnapshotAndGet(t *testing.T) {
	o := New()
	px := o.Snapshot()
	if len(px) < 1 {
		t.Fatal("snapshot 應至少含主流幣行情")
	}
	var btc bool
	for _, p := range px {
		if p.Symbol == "BTC/USDT" {
			btc = true
			if p.Price <= 0 {
				t.Fatalf("BTC 價格必須 > 0，got %v", p.Price)
			}
		}
	}
	if !btc {
		t.Fatal("快照應含 BTC/USDT")
	}
	p, ok := o.Get("btc/usdt")
	if !ok {
		t.Fatal("Get 應命中 BTC/USDT")
	}
	if p.Base != "BTC" || p.Price <= 0 {
		t.Fatalf("BTC 喂價異常: %+v", p)
	}
	if _, ok := o.Get("NOPE/USDT"); ok {
		t.Fatal("未知市場不應命中")
	}
}

func TestFeedManualOverride(t *testing.T) {
	o := New()
	if err := o.Feed("", 1); err == nil {
		t.Fatal("空符號應報錯")
	}
	if err := o.Feed("ETH/USDT", -5); err == nil {
		t.Fatal("負價格應報錯")
	}
	if err := o.Feed("ETH/USDT", 1234.5); err != nil {
		t.Fatalf("合法喂價應成功: %v", err)
	}
	p, ok := o.Get("eth/usdt")
	if !ok {
		t.Fatal("喂價後應可查詢")
	}
	if p.Price != 1234.5 {
		t.Fatalf("手動覆寫價應生效，got %v", p.Price)
	}
	if p.Source != "manual" {
		t.Fatalf("來源應為 manual，got %s", p.Source)
	}
	// 自訂符號（非主流錶）也可喂價。
	if err := o.Feed("TACM/USDT", 2.5); err != nil {
		t.Fatalf("自訂符號喂價應成功: %v", err)
	}
	p2, ok := o.Get("TACM/USDT")
	if !ok || p2.Price != 2.5 {
		t.Fatalf("自訂符號喂價異常: %+v", p2)
	}
}

func TestHistory(t *testing.T) {
	o := New()
	_ = o.Feed("SOL/USDT", 100)
	_ = o.Feed("SOL/USDT", 110)
	h := o.History("sol/usdt")
	if len(h) < 2 {
		t.Fatalf("歷史應至少 2 筆，got %d", len(h))
	}
	if h[0].Price != 110 {
		t.Fatalf("歷史應新→舊，首筆應為最新 110，got %v", h[0].Price)
	}
	if len(o.History("UNKNOWN")) != 0 {
		t.Fatal("未知市場歷史應為空")
	}
}

func TestStartAndCancel(t *testing.T) {
	o := New()
	ctx, cancel := context.WithCancel(context.Background())
	o.Start(ctx, 20*time.Millisecond)
	time.Sleep(60 * time.Millisecond)
	cancel()
	// 取消後 goroutine 應退出（不 panic）。
	if o.Uptime() < 0 {
		t.Fatal("uptime 異常")
	}
	_ = strings.ToUpper("ok")
}

// TestAggregateMedian M153：多 feeder 提交 → 中位數聚合；不足則沿用外部行情。
func TestAggregateMedian(t *testing.T) {
	o := New()
	const sym = "TACM/USDT"

	// 提交 3 筆 feeder 價格（離群值 1.5、中位數 2.0、極端 100）。
	for _, p := range []float64{1.5, 100, 2.0} {
		if err := o.SubmitFeeder("feeder-"+string(rune(int('a')+len(o.feederPool))), sym, p); err != nil {
			t.Fatalf("SubmitFeeder err: %v", err)
		}
	}
	// 非法提交應拒絕。
	if err := o.SubmitFeeder("f-x", sym, -1); err == nil {
		t.Fatal("負價格應拒絕")
	}

	px := o.AggregateRound()
	found := false
	for _, p := range px {
		if p.Symbol == sym {
			found = true
			if p.Source != "aggregate" {
				t.Fatalf("聚合後 source 應 aggregate，got %q", p.Source)
			}
			// 1.5, 2.0, 100 排序 → 中位數 2.0。
			if p.Price < 1.9 || p.Price > 2.1 {
				t.Fatalf("中位數應約 2.0，got %v", p.Price)
			}
		}
	}
	if !found {
		t.Fatal("聚合結果應含 TACM/USDT")
	}
	if r := o.Round(sym); r != 1 {
		t.Fatalf("round 應為 1，got %d", r)
	}

	// 聚合後下輪提交池應清空。
	if len(o.feederPool[sym]) != 0 {
		t.Fatal("聚合後提交池應清空")
	}
}

// TestStale M153：新鮮度檢查。
func TestStale(t *testing.T) {
	o := New()
	// 無資料 → stale。
	if !o.Stale("NOPE/USDT") {
		t.Fatal("無資料應視為 stale")
	}
	// 手動覆寫最新 → 新鮮。
	if err := o.Feed("TACM/USDT", 1.2); err != nil {
		t.Fatal(err)
	}
	if o.Stale("TACM/USDT") {
		t.Fatal("剛喂價應新鮮")
	}
}
