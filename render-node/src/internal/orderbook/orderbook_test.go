package orderbook

// orderbook_test.go — 核心機制短測：掛單／撤單／撮合／深度／手續費／錯誤路徑／價格時間優先。

import (
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestPlaceOrderValidationErrors(t *testing.T) {
	s := newTestStore(t)
	cases := []struct {
		name   string
		maker  string
		market string
		side   string
		typ    string
		price  float64
		amount float64
	}{
		{"empty_maker", "", "TACm/TiUSD", "buy", "limit", 1.0, 1},
		{"bad_side", "alice", "TACm/TiUSD", "long", "limit", 1.0, 1},
		{"bad_type", "alice", "TACm/TiUSD", "buy", "market", 1.0, 1},
		{"zero_price", "alice", "TACm/TiUSD", "buy", "limit", 0, 1},
		{"zero_amount", "alice", "TACm/TiUSD", "buy", "limit", 1.0, 0},
		{"bad_market", "alice", "BTC/USD", "buy", "limit", 1.0, 1},
		{"below_min", "alice", "TACm/TiUSD", "buy", "limit", 1.0, 0.0001},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := s.PlaceOrder(c.maker, c.market, c.side, c.typ, c.price, c.amount); err == nil {
				t.Fatalf("expected error for %s", c.name)
			}
		})
	}
}

func TestPlaceOrderNoMatchResting(t *testing.T) {
	s := newTestStore(t)
	res, err := s.PlaceOrder("alice", "TACm/TiUSD", "buy", "limit", 1.5, 10)
	if err != nil {
		t.Fatalf("place buy: %v", err)
	}
	if res.Order.Status != StatusOpen {
		t.Fatalf("status should be open (no asks), got %s", res.Order.Status)
	}
	if res.Remaining != 10 {
		t.Fatalf("remaining should be 10, got %v", res.Remaining)
	}
	if len(res.Fills) != 0 {
		t.Fatalf("no fills expected, got %d", len(res.Fills))
	}
}

func TestMatchBuyTakerCrossesAsk(t *testing.T) {
	s := newTestStore(t)
	// Seller places sell at 2.0 (resting maker).
	sell, err := s.PlaceOrder("carol", "TACm/TiUSD", "sell", "limit", 2.0, 5)
	if err != nil {
		t.Fatalf("place sell: %v", err)
	}
	if sell.Order.Status != StatusOpen {
		t.Fatalf("sell should rest open, got %s", sell.Order.Status)
	}

	// Buyer places aggressive buy at 2.5 (>= ask 2.0) → should fill at maker price 2.0.
	buy, err := s.PlaceOrder("alice", "TACm/TiUSD", "buy", "limit", 2.5, 3)
	if err != nil {
		t.Fatalf("place buy: %v", err)
	}
	if len(buy.Fills) != 1 {
		t.Fatalf("expected 1 fill, got %d", len(buy.Fills))
	}
	f := buy.Fills[0]
	if f.Price != 2.0 {
		t.Fatalf("fill price should be maker price 2.0, got %v", f.Price)
	}
	if f.Amount != 3 {
		t.Fatalf("fill amount should be 3, got %v", f.Amount)
	}
	if f.Maker != "carol" || f.Taker != "alice" {
		t.Fatalf("maker/taker wrong: %+v", f)
	}
	// Taker fee = 2.0*3*0.001 = 0.006; maker fee = 0 (default).
	if f.TakerFee < 0.0059 || f.TakerFee > 0.0061 {
		t.Fatalf("taker fee ~0.006, got %v", f.TakerFee)
	}
	if f.MakerFee != 0 {
		t.Fatalf("maker fee should be 0 (default), got %v", f.MakerFee)
	}
	// Taker buy fully filled.
	if buy.Order.Status != StatusFilled {
		t.Fatalf("buy should be filled, got %s", buy.Order.Status)
	}
	// Maker sell partially filled: 5-3=2 remaining.
	makerAfter, _ := s.GetOrder(sell.Order.ID)
	if makerAfter.Status != StatusPartial {
		t.Fatalf("sell maker should be partial, got %s", makerAfter.Status)
	}
	if makerAfter.Filled != 3 {
		t.Fatalf("sell filled should be 3, got %v", makerAfter.Filled)
	}
}

func TestPriceTimePriority(t *testing.T) {
	s := newTestStore(t)
	// Three resting sells: prices 2.2, 2.0, 2.0 (second one later).
	if _, err := s.PlaceOrder("s1", "TACm/TiUSD", "sell", "limit", 2.2, 2); err != nil {
		t.Fatal(err)
	}
	sA, _ := s.PlaceOrder("s2", "TACm/TiUSD", "sell", "limit", 2.0, 2)
	sB, _ := s.PlaceOrder("s3", "TACm/TiUSD", "sell", "limit", 2.0, 2)

	// Taker buys 3 at price 3.0. Should fill cheapest first:
	// 1) sA@2.0 qty=2 (earlier at price 2.0)
	// 2) sB@2.0 qty=1 (remaining 1)
	buy, err := s.PlaceOrder("buyer", "TACm/TiUSD", "buy", "limit", 3.0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(buy.Fills) != 2 {
		t.Fatalf("expected 2 fills, got %d: %+v", len(buy.Fills), buy.Fills)
	}
	if buy.Fills[0].MakerOrderID != sA.Order.ID {
		t.Fatalf("first fill should hit earlier sell sA (id=%d), got maker=%d", sA.Order.ID, buy.Fills[0].MakerOrderID)
	}
	if buy.Fills[1].MakerOrderID != sB.Order.ID {
		t.Fatalf("second fill should hit later sell sB (id=%d), got maker=%d", sB.Order.ID, buy.Fills[1].MakerOrderID)
	}
	if buy.Fills[1].Amount != 1 {
		t.Fatalf("second fill should be partial 1, got %v", buy.Fills[1].Amount)
	}
	// s1 at 2.2 should still be resting (not crossed).
	s1After, _ := s.GetOrder(s1OrderID(s))
	if s1After.Status != StatusOpen {
		t.Fatalf("s1@2.2 should still rest open, got %s", s1After.Status)
	}
}

// s1OrderID helper: re-list markets to grab first sell id (cheating but fine for test).
func s1OrderID(s *Store) int64 {
	rows, _ := s.db.Query(`SELECT id FROM ob_orders WHERE maker='s1' ORDER BY id ASC LIMIT 1`)
	defer rows.Close()
	var id int64
	if rows.Next() {
		_ = rows.Scan(&id)
	}
	return id
}

func TestCancelOrder(t *testing.T) {
	s := newTestStore(t)
	o, _ := s.PlaceOrder("alice", "TACm/TiUSD", "buy", "limit", 1.5, 10)

	// Wrong caller.
	if _, err := s.CancelOrder(o.Order.ID, "mallory"); err == nil {
		t.Fatalf("cancel by non-owner should fail")
	}
	// Correct cancel.
	cr, err := s.CancelOrder(o.Order.ID, "alice")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cr.Order.Status != StatusCanceled {
		t.Fatalf("status should be canceled, got %s", cr.Order.Status)
	}
	if cr.Remaining != 10 {
		t.Fatalf("remaining should be 10, got %v", cr.Remaining)
	}
	if cr.Order.Side != "buy" || cr.Order.Price != 1.5 {
		t.Fatalf("order side/price wrong: %+v", cr.Order)
	}

	// Sell order cancel.
	so, _ := s.PlaceOrder("bob", "TACm/TiUSD", "sell", "limit", 3.0, 4)
	cr2, err := s.CancelOrder(so.Order.ID, "bob")
	if err != nil {
		t.Fatalf("sell cancel: %v", err)
	}
	if cr2.Remaining != 4 {
		t.Fatalf("sell remaining should be 4, got %v", cr2.Remaining)
	}
	if cr2.Order.Side != "sell" {
		t.Fatalf("order side should be sell, got %s", cr2.Order.Side)
	}
}

func TestDepthAggregation(t *testing.T) {
	s := newTestStore(t)
	// Bids: two at 1.0 (qty 2+3=5), one at 0.9 (qty 1).
	_, _ = s.PlaceOrder("b1", "TACm/TiUSD", "buy", "limit", 1.0, 2)
	_, _ = s.PlaceOrder("b2", "TACm/TiUSD", "buy", "limit", 1.0, 3)
	_, _ = s.PlaceOrder("b3", "TACm/TiUSD", "buy", "limit", 0.9, 1)
	// Asks: one at 1.5 (qty 4), one at 1.6 (qty 2).
	_, _ = s.PlaceOrder("a1", "TACm/TiUSD", "sell", "limit", 1.5, 4)
	_, _ = s.PlaceOrder("a2", "TACm/TiUSD", "sell", "limit", 1.6, 2)

	d, err := s.Depth("TACm/TiUSD", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Bids) != 2 {
		t.Fatalf("expected 2 bid levels, got %d: %+v", len(d.Bids), d.Bids)
	}
	if d.Bids[0].Price != 1.0 || d.Bids[0].Amount != 5 || d.Bids[0].OrderCount != 2 {
		t.Fatalf("top bid wrong: %+v", d.Bids[0])
	}
	if d.Bids[1].Price != 0.9 || d.Bids[1].Amount != 1 {
		t.Fatalf("second bid wrong: %+v", d.Bids[1])
	}
	if len(d.Asks) != 2 {
		t.Fatalf("expected 2 ask levels, got %d: %+v", len(d.Asks), d.Asks)
	}
	if d.Asks[0].Price != 1.5 || d.Asks[0].Amount != 4 {
		t.Fatalf("lowest ask wrong: %+v", d.Asks[0])
	}
	if d.Asks[1].Price != 1.6 {
		t.Fatalf("second ask wrong: %+v", d.Asks[1])
	}
}

func TestPartialFillThenCancelRemaining(t *testing.T) {
	s := newTestStore(t)
	// Resting sell: 10 @ 2.0.
	rest, _ := s.PlaceOrder("carol", "TACm/TiUSD", "sell", "limit", 2.0, 10)
	// Taker buys 4.
	buy, _ := s.PlaceOrder("alice", "TACm/TiUSD", "buy", "limit", 2.0, 4)
	if buy.Order.Status != StatusFilled {
		t.Fatalf("buy should fill fully, got %s", buy.Order.Status)
	}
	// Carol cancels remaining sell (10-4=6 base).
	cr, err := s.CancelOrder(rest.Order.ID, "carol")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cr.Remaining != 6 {
		t.Fatalf("partial sell cancel should have remaining 6, got %v", cr.Remaining)
	}
	if cr.Order.Status != StatusCanceled {
		t.Fatalf("status should be canceled, got %s", cr.Order.Status)
	}
}

func TestMarketsList(t *testing.T) {
	s := newTestStore(t)
	ms, err := s.Markets()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].Symbol != "TACm/TiUSD" {
		t.Fatalf("expected seed market TACm/TiUSD, got %+v", ms)
	}
}

func TestRecentTradesRecorded(t *testing.T) {
	s := newTestStore(t)
	_, _ = s.PlaceOrder("carol", "TACm/TiUSD", "sell", "limit", 2.0, 5)
	_, _ = s.PlaceOrder("alice", "TACm/TiUSD", "buy", "limit", 2.0, 2)
	tr, err := s.RecentTrades("TACm/TiUSD", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(tr))
	}
	if tr[0].Amount != 2 || tr[0].Price != 2.0 {
		t.Fatalf("trade wrong: %+v", tr[0])
	}
}

func TestMyOrders(t *testing.T) {
	s := newTestStore(t)
	_, _ = s.PlaceOrder("alice", "TACm/TiUSD", "buy", "limit", 1.0, 1)
	_, _ = s.PlaceOrder("alice", "TACm/TiUSD", "sell", "limit", 3.0, 1)
	ords, err := s.MyOrders("alice", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(ords) != 2 {
		t.Fatalf("alice should have 2 orders, got %d", len(ords))
	}
}
