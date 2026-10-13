package exchange

import (
	"fmt"
	"sort"
)

// Order 為一張掛單（最小單位整數價格/數量）。
type Order struct {
	ID           int64       `json:"id"`
	UID          string      `json:"uid"`
	Market       string      `json:"market"`
	Side         Side        `json:"side"`
	Type         OrderType   `json:"type"`
	Price        Amount      `json:"-"` // 限價單：目標價；市價單：0（以對端價成交）
	Qty          Amount      `json:"-"`
	Filled       Amount      `json:"-"`
	Status       OrderStatus `json:"status"`
	CreatedTs    int64       `json:"created_ts"`
	PriceStr     string      `json:"price"`
	QtyStr       string      `json:"qty"`
	FilledStr    string      `json:"filled"`
	RemainingStr string      `json:"remaining"`
	QuoteBase    Asset       `json:"-"`
	QuoteQuote   Asset       `json:"-"`
}

// remaining 返回剩餘數量。
func (o *Order) remaining() Amount { return o.Qty.Sub(o.Filled) }

// PriceLevel 為訂單簿一檔。
type PriceLevel struct {
	Price      Amount `json:"-"`
	Qty        Amount `json:"-"`
	PriceStr   string `json:"price"`
	QtyStr     string `json:"qty"`
	OrderCount int    `json:"order_count"`
	PriceAsset Asset  `json:"-"`
	QtyAsset   Asset  `json:"-"`
}

// OrderBook 為價格-時間優先的訂單簿（buy 降序、sell 升序聚合）。
type OrderBook struct {
	Base  Asset
	Quote Asset
	bids  []*Order // 買單（價高優先）
	asks  []*Order // 賣單（價低優先）
}

// NewOrderBook 建空簿。
func NewOrderBook(m Market) *OrderBook {
	return &OrderBook{Base: m.Base, Quote: m.Quote}
}

// BestBid / BestAsk 返回最佳價格（無單時 nil）。
func (ob *OrderBook) BestBid() *Amount {
	if len(ob.bids) == 0 {
		return nil
	}
	best := ob.bids[0].Price
	return &best
}
func (ob *OrderBook) BestAsk() *Amount {
	if len(ob.asks) == 0 {
		return nil
	}
	best := ob.asks[0].Price
	return &best
}

// insert 按價格-時間優先插入（buy：價高在前；sell：價低在前）。
func (ob *OrderBook) insert(o *Order) {
	if o.Side == SideBuy {
		i := sort.Search(len(ob.bids), func(i int) bool { return ob.bids[i].Price.Cmp(o.Price) < 0 })
		ob.bids = append(ob.bids, nil)
		copy(ob.bids[i+1:], ob.bids[i:])
		ob.bids[i] = o
	} else {
		i := sort.Search(len(ob.asks), func(i int) bool { return ob.asks[i].Price.Cmp(o.Price) > 0 })
		ob.asks = append(ob.asks, nil)
		copy(ob.asks[i+1:], ob.asks[i:])
		ob.asks[i] = o
	}
}

// remove 移除指定訂單（返回是否移除）。
func (ob *OrderBook) remove(id int64) bool {
	for i, o := range ob.bids {
		if o.ID == id {
			ob.bids = append(ob.bids[:i], ob.bids[i+1:]...)
			return true
		}
	}
	for i, o := range ob.asks {
		if o.ID == id {
			ob.asks = append(ob.asks[:i], ob.asks[i+1:]...)
			return true
		}
	}
	return false
}

// Depth 返回聚合檔位（買單最高 N 檔 + 賣單最低 N 檔）。
func (ob *OrderBook) Depth(n int) (bids, asks []PriceLevel) {
	if n <= 0 {
		n = 10
	}
	bids = aggregateLevels(ob.bids, n, false)
	asks = aggregateLevels(ob.asks, n, true)
	return
}

// aggregateLevels 聚合同價訂單；ascending=true（賣單）時由低到高。
func aggregateLevels(orders []*Order, n int, ascending bool) []PriceLevel {
	out := make([]PriceLevel, 0, n)
	for _, o := range orders {
		if o.remaining().IsZero() {
			continue
		}
		if len(out) == 0 || out[len(out)-1].Price.Cmp(o.Price) != 0 {
			out = append(out, PriceLevel{
				Price: o.Price, Qty: o.remaining(), OrderCount: 1,
				PriceStr: o.Price.String(o.QuoteQuote), QtyStr: o.remaining().String(o.QuoteBase),
				PriceAsset: o.QuoteQuote, QtyAsset: o.QuoteBase,
			})
		} else {
			last := &out[len(out)-1]
			last.Qty = last.Qty.Add(o.remaining())
			last.QtyStr = last.Qty.String(o.QuoteBase)
			last.OrderCount++
		}
		if len(out) >= n {
			break
		}
	}
	return out
}

// String 為調試用深度文本。
func (ob *OrderBook) String(levels int) string {
	bids, asks := ob.Depth(levels)
	s := fmt.Sprintf("=== %s/%s 訂單簿 ===\n", ob.Base, ob.Quote)
	s += "-- asks (sell, 低→高) --\n"
	for i := len(asks) - 1; i >= 0; i-- {
		a := asks[i]
		s += fmt.Sprintf("  賣  %s @ %s (%d 單)\n", a.QtyStr, a.PriceStr, a.OrderCount)
	}
	s += "-- bids (buy, 高→低) --\n"
	for _, b := range bids {
		s += fmt.Sprintf("  買  %s @ %s (%d 單)\n", b.QtyStr, b.PriceStr, b.OrderCount)
	}
	return s
}
