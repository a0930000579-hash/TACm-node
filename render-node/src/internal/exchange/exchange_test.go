package exchange

import (
	"strings"
	"testing"
	"time"
)

func exAmt(s string, a Asset) Amount {
	v, err := NewAmount(s, a)
	if err != nil {
		panic(err)
	}
	return v
}

func newSvc(t *testing.T) *Service {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewService(st)
}

// TestLimitMatch：TACM/USDT 限價撮合（價格-時間優先 + 手續費結算）。
func TestLimitMatch(t *testing.T) {
	svc := newSvc(t)
	m, _ := ParseMarket("TACM/USDT")
	alice, bob := "tx0Alice", "tx0Bob"

	// bob 掛賣 2 TACM @ 100 USDT；alice 買 1 TACM @ 100。
	if err := svc.Deposit(bob, AssetTACm, exAmt("2", AssetTACm), ""); err != nil {
		t.Fatal(err)
	}
	sell, err := svc.PlaceOrder(bob, m, SideSell, TypeLimit, exAmt("100", AssetUSDT), exAmt("2", AssetTACm))
	if err != nil {
		t.Fatal(err)
	}
	if len(sell.Trades) != 0 || sell.Order.Status != StatusOpen {
		t.Fatalf("賣單應留簿: trades=%d status=%s", len(sell.Trades), sell.Order.Status)
	}

	if err := svc.Deposit(alice, AssetUSDT, exAmt("100", AssetUSDT), ""); err != nil {
		t.Fatal(err)
	}
	buy, err := svc.PlaceOrder(alice, m, SideBuy, TypeLimit, exAmt("100", AssetUSDT), exAmt("1", AssetTACm))
	if err != nil {
		t.Fatal(err)
	}
	if len(buy.Trades) != 1 {
		t.Fatalf("買單應成交 1 筆: %d", len(buy.Trades))
	}
	tr := buy.Trades[0]
	// taker=alice 買，fee=1×1.25%=0.0125 USDT（quote 計）；maker=bob 賣，fee=100×0.625%=0.625。
	if tr.PriceStr != "100" || tr.QtyStr != "1" {
		t.Fatalf("成交價量異常: %s @ %s", tr.QtyStr, tr.PriceStr)
	}
	// taker 買單手續費從所得 base（TACM）扣：1×1.25% = 0.0125 TACM。
	if tr.TakerFee.String(AssetTACm) != "0.0125" {
		t.Fatalf("taker fee=%s want 0.0125 TACM", tr.TakerFee.String(AssetTACm))
	}
	if tr.MakerFee.String(AssetUSDT) != "0.625" {
		t.Fatalf("maker fee=%s want 0.625", tr.MakerFee.String(AssetUSDT))
	}
	// alice 得 0.9875 TACM（扣費）。
	ab, _ := svc.Balances(alice)
	for _, b := range ab {
		if b.Asset == AssetTACm && b.AvailStr != "0.9875" {
			t.Fatalf("alice TACM=%s want 0.9875", b.AvailStr)
		}
		if b.Asset == AssetUSDT && b.AvailStr != "0" {
			t.Fatalf("alice USDT 應清空: %s", b.AvailStr)
		}
	}
	// bob 得 100-0.625=99.375 USDT，剩 1 TACM 未成交（還在簿）。
	bb, _ := svc.Balances(bob)
	for _, b := range bb {
		if b.Asset == AssetUSDT && b.AvailStr != "99.375" {
			t.Fatalf("bob USDT=%s want 99.375", b.AvailStr)
		}
	}
	// 訂單簿：賣單剩 1 TACM。
	bids, asks, _ := svc.OrderBook(m, 5)
	if len(asks) != 1 || asks[0].QtyStr != "1" {
		t.Fatalf("賣單剩餘應為 1 TACM: %+v", asks)
	}
	_ = bids
}

// TestPriceTimePriority：同價先到先成交。
func TestPriceTimePriority(t *testing.T) {
	svc := newSvc(t)
	m, _ := ParseMarket("TiUSD/USDT")
	a1, a2, b := "tx0P1", "tx0P2", "tx0Maker"
	for _, u := range []string{a1, a2} {
		if err := svc.Deposit(u, AssetUSDT, exAmt("1000", AssetUSDT), ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.Deposit(b, AssetTiUSD, exAmt("10", AssetTiUSD), ""); err != nil {
		t.Fatal(err)
	}
	// 兩個買單同價 1.0：a1 先、a2 後。
	if _, err := svc.PlaceOrder(a1, m, SideBuy, TypeLimit, exAmt("1", AssetUSDT), exAmt("3", AssetTiUSD)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := svc.PlaceOrder(a2, m, SideBuy, TypeLimit, exAmt("1", AssetUSDT), exAmt("3", AssetTiUSD)); err != nil {
		t.Fatal(err)
	}
	// 賣單 10 TiUSD @ 1.0 → 先吃 a1（3）再吃 a2（3）。
	sell, err := svc.PlaceOrder(b, m, SideSell, TypeLimit, exAmt("1", AssetUSDT), exAmt("6", AssetTiUSD))
	if err != nil {
		t.Fatal(err)
	}
	if len(sell.Trades) != 2 {
		t.Fatalf("應兩筆成交: %d", len(sell.Trades))
	}
	if sell.Trades[0].MakerUID != a1 || sell.Trades[1].MakerUID != a2 {
		t.Fatalf("時間優先失效: %s 先於 %s", sell.Trades[0].MakerUID, sell.Trades[1].MakerUID)
	}
}

// TestTiUSDFeePreference：TiUSD 報價費率最優（taker 0.5%）。
func TestTiUSDFeePreference(t *testing.T) {
	svc := newSvc(t)
	m, _ := ParseMarket("TACM/TiUSD")
	alice, bob := "tx0TA", "tx0TB"
	if err := svc.Deposit(bob, AssetTACm, exAmt("1", AssetTACm), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PlaceOrder(bob, m, SideSell, TypeLimit, exAmt("1", AssetTiUSD), exAmt("1", AssetTACm)); err != nil {
		t.Fatal(err)
	}
	if err := svc.Deposit(alice, AssetTiUSD, exAmt("1", AssetTiUSD), ""); err != nil {
		t.Fatal(err)
	}
	buy, err := svc.PlaceOrder(alice, m, SideBuy, TypeLimit, exAmt("1", AssetTiUSD), exAmt("1", AssetTACm))
	if err != nil {
		t.Fatal(err)
	}
	tr := buy.Trades[0]
	if tr.TakerFee.String(AssetTACm) != "0.005" {
		t.Fatalf("TiUSD taker fee=%s want 0.005 TACM", tr.TakerFee.String(AssetTACm))
	}
}

// TestMarketOrder：市價單按對端價全部成交；無深度失敗。
func TestMarketOrder(t *testing.T) {
	svc := newSvc(t)
	m, _ := ParseMarket("TACM/USDT")
	if err := svc.Deposit("tx0MK", AssetTACm, exAmt("1", AssetTACm), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PlaceOrder("tx0MK", m, SideSell, TypeLimit, exAmt("50", AssetUSDT), exAmt("1", AssetTACm)); err != nil {
		t.Fatal(err)
	}
	if err := svc.Deposit("tx0MR", AssetUSDT, exAmt("60", AssetUSDT), ""); err != nil {
		t.Fatal(err)
	}
	res, err := svc.PlaceOrder("tx0MR", m, SideBuy, TypeMarket, Amount{}, exAmt("1", AssetTACm))
	if err != nil {
		t.Fatal(err)
	}
	if res.Order.Status != StatusFilled || len(res.Trades) != 1 {
		t.Fatalf("市價單應全成交: %s trades=%d", res.Order.Status, len(res.Trades))
	}
	// 無深度市價單失敗。
	if err := svc.Deposit("tx0MR", AssetUSDT, exAmt("1", AssetUSDT), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PlaceOrder("tx0MR", m, SideBuy, TypeMarket, Amount{}, exAmt("0.5", AssetTACm)); err == nil {
		t.Fatal("無深度市價單應失敗")
	}
}

// TestCancelOrder：撤單歸還鎖定。
func TestCancelOrder(t *testing.T) {
	svc := newSvc(t)
	m, _ := ParseMarket("TACM/USDT")
	if err := svc.Deposit("tx0CX", AssetUSDT, exAmt("100", AssetUSDT), ""); err != nil {
		t.Fatal(err)
	}
	res, err := svc.PlaceOrder("tx0CX", m, SideBuy, TypeLimit, exAmt("10", AssetUSDT), exAmt("5", AssetTACm))
	if err != nil {
		t.Fatal(err)
	}
	bal, _ := svc.Balances("tx0CX")
	for _, b := range bal {
		if b.Asset == AssetUSDT && (b.AvailStr != "50" || b.LockedStr != "50") {
			t.Fatalf("鎖定異常: avail=%s locked=%s", b.AvailStr, b.LockedStr)
		}
	}
	_, err = svc.CancelOrder("tx0CX", res.Order.ID)
	if err != nil {
		t.Fatal(err)
	}
	bal, _ = svc.Balances("tx0CX")
	for _, b := range bal {
		if b.Asset == AssetUSDT && b.AvailStr != "100" {
			t.Fatalf("撤單未歸還鎖定: %s", b.AvailStr)
		}
	}
	// 非本人不可撤。
	if err := svc.Deposit("tx0CX2", AssetUSDT, exAmt("1", AssetUSDT), ""); err != nil {
		t.Fatal(err)
	}
	res2, _ := svc.PlaceOrder("tx0CX2", m, SideBuy, TypeLimit, exAmt("1", AssetUSDT), exAmt("1", AssetTACm))
	if _, err := svc.CancelOrder("tx0CX", res2.Order.ID); err == nil {
		t.Fatal("非本人撤單應失敗")
	}
}

// TestFlashSwap：TiUSD→USDT 閃兌（先掛簿提供流動性）。
func TestFlashSwap(t *testing.T) {
	svc := newSvc(t)
	m, _ := ParseMarket("TiUSD/USDT")
	if err := svc.Deposit("tx0MM", AssetTiUSD, exAmt("10", AssetTiUSD), ""); err != nil {
		t.Fatal(err)
	}
	if err := svc.Deposit("tx0MM", AssetUSDT, exAmt("10", AssetUSDT), ""); err != nil {
		t.Fatal(err)
	}
	// 掛買 10 TiUSD @ 1.0 USDT 提供買方流動性（閃兌賣 TiUSD 吃 bid）。
	if _, err := svc.PlaceOrder("tx0MM", m, SideBuy, TypeLimit, exAmt("1", AssetUSDT), exAmt("10", AssetTiUSD)); err != nil {
		t.Fatal(err)
	}
	if err := svc.Deposit("tx0SW", AssetTiUSD, exAmt("2", AssetTiUSD), ""); err != nil {
		t.Fatal(err)
	}
	res, err := svc.FlashSwap("tx0SW", AssetTiUSD, AssetUSDT, exAmt("2", AssetTiUSD))
	if err != nil {
		t.Fatal(err)
	}
	// 賣 2 TiUSD 得 2×1.0=2 USDT，費 2×1.25%=0.025（費以 USDT 計）。
	if res.ToStr != "1.975" {
		t.Fatalf("閃兌所得=%s want 1.975 USDT", res.ToStr)
	}
	if res.FeeStr != "0.025" {
		t.Fatalf("閃兌費=%s want 0.025", res.FeeStr)
	}
	bal, _ := svc.Balances("tx0SW")
	for _, b := range bal {
		if b.Asset == AssetTiUSD && b.AvailStr != "0" {
			t.Fatalf("閃兌後 TiUSD 應為 0: %s", b.AvailStr)
		}
		if b.Asset == AssetUSDT && b.AvailStr != "1.975" {
			t.Fatalf("閃兌後 USDT=%s want 1.975", b.AvailStr)
		}
	}
}

// TestBot：機器人掛雙側訂單並可停止。
func TestBot(t *testing.T) {
	svc := newSvc(t)
	m, _ := ParseMarket("TACM/USDT")
	if err := svc.Deposit("tx0BOT", AssetUSDT, exAmt("10000", AssetUSDT), ""); err != nil {
		t.Fatal(err)
	}
	if err := svc.Deposit("tx0BOT", AssetTACm, exAmt("10", AssetTACm), ""); err != nil {
		t.Fatal(err)
	}
	bot, err := svc.StartBot(BotConfig{
		UID: "tx0BOT", Market: "TACM/USDT", MidPrice: "100", SpreadPct: 20,
		Qty: "1", IntervalSec: 1, CancelOnRefresh: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	// 機器人應在簿上留有買/賣單。
	bids, asks, _ := svc.OrderBook(m, 5)
	if len(bids) == 0 || len(asks) == 0 {
		t.Fatalf("機器人應掛雙側單: bids=%d asks=%d", len(bids), len(asks))
	}
	bots := svc.ListBots()
	if len(bots) == 0 || len(bots[0].OrderIDs) == 0 {
		t.Fatal("機器人未記錄掛單")
	}
	st, err := svc.StopBot(bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "stopped" {
		t.Fatalf("機器人未停止: %s", st.Status)
	}
	// 停止後簿上應無該機器人訂單。
	bids, asks, _ = svc.OrderBook(m, 5)
	if len(bids)+len(asks) > 0 {
		t.Fatalf("停止後應撤單: %s", svc.book(m).String(3))
	}
}

// TestBigAmountNoOverflow：大額 TACm 交易不溢出（big.Int）。
func TestBigAmountNoOverflow(t *testing.T) {
	svc := newSvc(t)
	m, _ := ParseMarket("TACM/USDT")
	bob, alice := "tx0BigB", "tx0BigA"
	bigQty := strings.Repeat("9", 3) // 999 TACM > 9.2 上限
	if err := svc.Deposit(bob, AssetTACm, exAmt(bigQty, AssetTACm), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PlaceOrder(bob, m, SideSell, TypeLimit, exAmt("100", AssetUSDT), exAmt(bigQty, AssetTACm)); err != nil {
		t.Fatal(err)
	}
	if err := svc.Deposit(alice, AssetUSDT, exAmt("99900", AssetUSDT), ""); err != nil {
		t.Fatal(err)
	}
	res, err := svc.PlaceOrder(alice, m, SideBuy, TypeLimit, exAmt("100", AssetUSDT), exAmt(bigQty, AssetTACm))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Trades) != 1 {
		t.Fatalf("大額成交異常: %d", len(res.Trades))
	}
	ab, _ := svc.Balances(alice)
	for _, b := range ab {
		if b.Asset == AssetTACm && b.AvailStr != "986.5125" {
			t.Fatalf("alice TACM=%s want 986.5125 (999-999×1.25%%)", b.AvailStr)
		}
	}
}

// TestKlinesAndMyTrades：M101 K 線聚合與個人成交過濾。
func TestKlinesAndMyTrades(t *testing.T) {
	svc := newSvc(t)
	m, _ := ParseMarket("TACM/USDT")
	alice, bob := "tx0Alice", "tx0Bob"

	// bob 掛賣 2 TACM @ 100 USDT；alice 買 2 TACM @ 100 → 全成交。
	if err := svc.Deposit(bob, AssetTACm, exAmt("2", AssetTACm), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PlaceOrder(bob, m, SideSell, TypeLimit, exAmt("100", AssetUSDT), exAmt("2", AssetTACm)); err != nil {
		t.Fatal(err)
	}
	if err := svc.Deposit(alice, AssetUSDT, exAmt("200", AssetUSDT), ""); err != nil {
		t.Fatal(err)
	}
	res, err := svc.PlaceOrder(alice, m, SideBuy, TypeLimit, exAmt("100", AssetUSDT), exAmt("2", AssetTACm))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Trades) == 0 {
		t.Fatal("預期成交但無 trades")
	}
	// K 線聚合（60 秒 bucket）。
	kl, err := svc.Klines("TACM/USDT", 60, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(kl) == 0 {
		t.Fatal("K 線應有 1 根")
	}
	k := kl[len(kl)-1]
	if k.Open != 100 || k.Close != 100 || k.High != 100 || k.Low != 100 {
		t.Fatalf("K 線 OHLC 錯誤: %+v", k)
	}
	if k.Volume != 2 {
		t.Fatalf("K 線成交量應為 2: %f", k.Volume)
	}
	// 個人成交：alice 有 taker 紀錄、bob 有 maker 紀錄、路人無紀錄。
	at, err := svc.MyTrades(alice, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(at) == 0 || at[0].TakerUID != alice {
		t.Fatalf("alice 應有 taker 成交: %+v", at)
	}
	bt, err := svc.MyTrades(bob, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(bt) == 0 || bt[0].MakerUID != bob {
		t.Fatalf("bob 應有 maker 成交: %+v", bt)
	}
	ct, err := svc.MyTrades("tx0Nobody", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(ct) != 0 {
		t.Fatalf("路人應無成交: %+v", ct)
	}
	// market 過濾。
	mt, err := svc.MyTrades(alice, "TIUSD/USDT", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(mt) != 0 {
		t.Fatalf("market 過濾應為空: %+v", mt)
	}
}
