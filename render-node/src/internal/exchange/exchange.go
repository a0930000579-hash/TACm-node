package exchange

import (
	"fmt"
	"math/big"
	"sync"
	"time"
)

// FeeUID 交易所手續費帳戶（撮合手續費集中挹注，可提現至鏈上）。
const FeeUID = "fee"

// Service 為交易所核心：餘額簿 + 撮合引擎 + 閃兌 + 自動交易機器人。
// 撮合規則（對照 Python exchange_match.py）：
//   - 價格-時間優先：買單價高者先、同價先到者先；賣單價低者先
//   - taker 吃 maker：成交價以 maker 價格為準
//   - 手續費：quote 資產（TiUSD 50/25bp、USDT 125/63bp、其餘 10/5bp），taker 與 maker 分開
//   - 限價單：能吃多少吃多少，剩餘留簿；市價單：按對端價全部成交（簿無對端則失敗）
//   - 餘額：下單先鎖定（avail→locked），成交結算後解鎖剩餘/入帳
type Service struct {
	st    *Store
	mu    sync.Mutex // 撮合與餘額操作串行化（Go 慣用：單一寫鎖）
	books map[string]*OrderBook
	bots  map[string]*Bot
}

// NewService 創建交易所服務。
func NewService(st *Store) *Service {
	return &Service{
		st:    st,
		books: map[string]*OrderBook{},
		bots:  map[string]*Bot{},
	}
}

// Store 返回底層存儲。
func (s *Service) Store() *Store { return s.st }

// book 取得（或建立）訂單簿。
func (s *Service) book(m Market) *OrderBook {
	key := m.String()
	if b, ok := s.books[key]; ok {
		return b
	}
	b := NewOrderBook(m)
	s.books[key] = b
	return b
}

// PlaceResult 為下單結果。
type PlaceResult struct {
	Order        *Order   `json:"order"`
	Trades       []*Trade `json:"trades"`
	FilledQty    Amount   `json:"-"`
	FilledQtyStr string   `json:"filled_qty"`
}

// PlaceOrder 下單（限價/市價）。先鎖定餘額，再撮合，剩餘留簿/失敗退鎖。
func (s *Service) PlaceOrder(uid string, m Market, side Side, typ OrderType, price, qty Amount) (*PlaceResult, error) {
	if uid == "" || !m.Supported() || !side.Valid() || !typ.Valid() {
		return nil, fmt.Errorf("exchange: 參數非法 (uid=%q market=%s side=%s type=%s)", uid, m, side, typ)
	}
	if qty.Cmp(Amount{}) <= 0 {
		return nil, fmt.Errorf("exchange: 數量必須大於 0")
	}
	if typ == TypeLimit && price.Cmp(Amount{}) <= 0 {
		return nil, fmt.Errorf("exchange: 限價必須大於 0")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// 防自我交易：同 uid 的對側交叉訂單存在時拒絕下單（禁止 wash trade）。
	if typ == TypeLimit {
		ob := s.book(m)
		blocked := false
		if side == SideBuy {
			for _, a := range ob.asks {
				if a.UID == uid && (a.Status == StatusOpen || a.Status == StatusPartiallyFilled) && a.Price.Cmp(price) <= 0 {
					blocked = true
					break
				}
			}
		} else {
			for _, b := range ob.bids {
				if b.UID == uid && (b.Status == StatusOpen || b.Status == StatusPartiallyFilled) && b.Price.Cmp(price) >= 0 {
					blocked = true
					break
				}
			}
		}
		if blocked {
			return nil, fmt.Errorf("exchange: 禁止自我交易（%s 已存在對側交叉訂單，請撤單後再掛）", uid)
		}
	}

	// 鎖定餘額：買單鎖 quote（qty×price），賣單鎖 base（qty）。
	var lockAmt Amount
	if side == SideBuy {
		lockAmt = mulPrice(qty, price, m.Base.Decimals())
		if err := s.st.lock(uid, m.Quote, lockAmt); err != nil {
			return nil, err
		}
	} else {
		lockAmt = qty
		if err := s.st.lock(uid, m.Base, lockAmt); err != nil {
			return nil, err
		}
	}

	o := &Order{
		UID: uid, Market: m.String(), Side: side, Type: typ,
		Price: price, Qty: qty, Status: StatusOpen,
		CreatedTs: time.Now().Unix(), QuoteBase: m.Base, QuoteQuote: m.Quote,
	}
	id, err := s.st.insertOrder(o)
	if err != nil {
		return nil, err
	}
	o.ID = id
	o.PriceStr, o.QtyStr = price.String(m.Quote), qty.String(m.Base)

	res := &PlaceResult{Order: o}
	// 市價單無掛單價：不進簿，僅以對端價撮合。
	if typ == TypeMarket {
		trades, filled, err := s.matchTaker(o, m, true)
		if err != nil {
			// 成交失敗：退鎖、撤單。
			_ = s.st.unlock(uid, sideLockAsset(side, m), lockAmt)
			o.Status = StatusCanceled
			_ = s.st.updateOrder(o)
			return nil, err
		}
		res.Trades, res.FilledQty = trades, filled
		res.FilledQtyStr = filled.String(m.Base)
		o.Filled = filled
		if filled.Cmp(qty) == 0 {
			o.Status = StatusFilled
			_ = s.st.unlock(uid, sideLockAsset(side, m), lockAmt.Sub(filledLock(side, filled, price, m.Base.Decimals())))
		} else {
			o.Status = StatusPartiallyFilled
		}
		_ = s.st.updateOrder(o)
		return res, nil
	}

	// 限價單：撮合。
	trades, filled, err := s.matchTaker(o, m, false)
	if err != nil {
		_ = s.st.unlock(uid, sideLockAsset(side, m), lockAmt)
		o.Status = StatusCanceled
		_ = s.st.updateOrder(o)
		return nil, err
	}
	res.Trades, res.FilledQty = trades, filled
	res.FilledQtyStr = filled.String(m.Base)
	o.Filled = filled
	if filled.Cmp(qty) == 0 {
		o.Status = StatusFilled
		_ = s.st.unlock(uid, sideLockAsset(side, m), lockAmt.Sub(filledLock(side, filled, price, m.Base.Decimals())))
	} else if filled.Cmp(Amount{}) > 0 {
		o.Status = StatusPartiallyFilled
		// 剩餘留簿（部分成交後剩餘保持原價位）。
		s.book(m).insert(o)
		// 剩餘對應的鎖定餘額保留在 locked。
		_ = s.st.updateOrder(o)
	} else {
		// 未成交，整單留簿。
		s.book(m).insert(o)
	}
	return res, nil
}

// sideLockAsset 返回鎖定資產（買：quote；賣：base）。
func sideLockAsset(side Side, m Market) Asset {
	if side == SideBuy {
		return m.Quote
	}
	return m.Base
}

// filledLock 返回成交對應的鎖定消耗（買：filled×price；賣：filled）。
func filledLock(side Side, filled Amount, price Amount, baseDec int) Amount {
	if side == SideBuy {
		return mulPrice(filled, price, baseDec)
	}
	return filled
}

// matchTaker 撮合 taker 訂單（按 maker 價成交），回傳成交明細與已成交數量。
// consumeAll=true（市價）時對端無簿即失敗；false（限價）時無可成交即返回空。
func (s *Service) matchTaker(taker *Order, m Market, consumeAll bool) ([]*Trade, Amount, error) {
	ob := s.book(m)
	fr := m.FeeRates()
	trades := []*Trade{}
	var filledQty Amount

	// 選擇對端佇列（用指標：ob.remove 更新佇列後同步反映）。
	var makers *[]*Order
	if taker.Side == SideBuy {
		makers = &ob.asks
	} else {
		makers = &ob.bids
	}

	guard := 0
	for len(*makers) > 0 {
		maker := (*makers)[0]
		guard++
		if guard > 100000 {
			return trades, filledQty, fmt.Errorf("exchange: 撮合迭代上限（狀態異常）maker=%s status=%s qty=%s filled=%s",
				maker.UID, maker.Status, maker.Qty.String(m.Base), maker.Filled.String(m.Base))
		}
		if maker.Status != StatusOpen && maker.Status != StatusPartiallyFilled {
			ob.remove(maker.ID)
			continue
		}
		// 限價單價格檢查：買單價 ≥ maker 價、賣單價 ≤ maker 價。
		if taker.Type == TypeLimit {
			if taker.Type != TypeMarket && taker.Side == SideBuy && taker.Price.Cmp(maker.Price) < 0 {
				break
			}
			if taker.Type != TypeMarket && taker.Side == SideSell && taker.Price.Cmp(maker.Price) > 0 {
				break
			}
		}
		// 防自我交易保底（市價/閃兌路徑）：僅對「本可成交」的自身對側單跳過。
		if maker.UID == taker.UID {
			ob.remove(maker.ID)
			continue
		}
		makerRem := maker.remaining()
		takerRem := taker.Qty.Sub(taker.Filled)
		if takerRem.Cmp(Amount{}) <= 0 {
			break
		}
		// 成交數量 = min(雙方剩餘)。
		execQty := makerRem
		if takerRem.Cmp(makerRem) < 0 {
			execQty = takerRem
		}
		price := maker.Price
		tr, err := s.settleTrade(taker, maker, price, execQty, fr)
		if err != nil {
			return trades, filledQty, err
		}
		trades = append(trades, tr)
		filledQty = filledQty.Add(execQty)

		maker.Filled = maker.Filled.Add(execQty)
		taker.Filled = taker.Filled.Add(execQty)
		if maker.remaining().IsZero() {
			maker.Status = StatusFilled
			// 消耗 maker 剩餘鎖定（資產已全額兌付給 taker，不可歸還）。
			if err := s.st.consumeLocked(maker.UID, sideLockAsset(maker.Side, m), filledLock(maker.Side, maker.Qty, maker.Price, m.Base.Decimals())); err != nil {
				return trades, filledQty, err
			}
		} else {
			maker.Status = StatusPartiallyFilled
		}
		_ = s.st.updateOrder(maker)
		if maker.remaining().IsZero() {
			ob.remove(maker.ID)
		}
		if takerRem.Cmp(execQty) == 0 {
			break
		}
	}
	// 市價單未完全成交：失敗。
	if consumeAll && taker.remaining().Cmp(Amount{}) > 0 {
		return trades, filledQty, fmt.Errorf("exchange: 市價單無法完全成交（簿深度不足）")
	}
	return trades, filledQty, nil
}

// settleTrade 結算一筆成交：
//   - taker 買：taker 付 quote（price×qty），得 base×(1-taker_fee)；maker 付 base，得 quote×(1-maker_fee)
//   - taker 賣：taker 付 base，得 quote×(1-taker_fee)；maker 付 quote，得 base×(1-maker_fee)
//
// 鎖定餘額釋放（locked→avail）＋收款入帳。
func (s *Service) settleTrade(taker, maker *Order, price, qty Amount, fr FeeRates) (*Trade, error) {
	quoteAmt := mulPrice(qty, price, taker.QuoteBase.Decimals())
	tr := &Trade{
		Market: taker.Market, Ts: time.Now().Unix(), TakerSide: taker.Side,
		Price: price, Qty: qty, Quote: quoteAmt,
		TakerUID: taker.UID, MakerUID: maker.UID,
		TakerOrderID: taker.ID, MakerOrderID: maker.ID,
	}
	m := Market{Base: taker.QuoteBase, Quote: taker.QuoteQuote}
	if taker.Side == SideBuy {
		// 結算 taker（買）：市價單直接扣 avail（無預鎖）；限價單消耗已鎖定的 quote（不歸還）。
		if taker.Type == TypeMarket {
			if err := s.st.debit(taker.UID, m.Quote, quoteAmt); err != nil {
				return nil, err
			}
		} else if err := s.st.consumeLocked(taker.UID, m.Quote, quoteAmt); err != nil {
			return nil, err
		}
		takerFee := qty.MulPpm(fr.TakerPpm)
		tr.TakerFee = takerFee
		if err := s.st.credit(taker.UID, m.Base, qty.Sub(takerFee)); err != nil {
			return nil, err
		}
		// 手續費入 fee 帳戶（taker 費為 base、maker 費為 quote）。
		if err := s.st.credit(FeeUID, m.Base, takerFee); err != nil {
			return nil, err
		}
		// 結算 maker（賣）：收款；base 鎖定在訂單完全成交/撤單時一次釋放。
		makerFee := quoteAmt.MulPpm(fr.MakerPpm)
		tr.MakerFee = makerFee
		if err := s.st.credit(maker.UID, m.Quote, quoteAmt.Sub(makerFee)); err != nil {
			return nil, err
		}
		if err := s.st.credit(FeeUID, m.Quote, makerFee); err != nil {
			return nil, err
		}
	} else {
		// taker 賣：市價單直接扣 avail；限價單消耗已鎖定的 base。
		if taker.Type == TypeMarket {
			if err := s.st.debit(taker.UID, m.Base, qty); err != nil {
				return nil, err
			}
		} else if err := s.st.consumeLocked(taker.UID, m.Base, qty); err != nil {
			return nil, err
		}
		takerFee := quoteAmt.MulPpm(fr.TakerPpm)
		tr.TakerFee = takerFee
		if err := s.st.credit(taker.UID, m.Quote, quoteAmt.Sub(takerFee)); err != nil {
			return nil, err
		}
		if err := s.st.credit(FeeUID, m.Quote, takerFee); err != nil {
			return nil, err
		}
		// maker 買：收款；quote 鎖定在訂單完全成交/撤單時一次釋放。
		makerFee := qty.MulPpm(fr.MakerPpm)
		tr.MakerFee = makerFee
		if err := s.st.credit(maker.UID, m.Base, qty.Sub(makerFee)); err != nil {
			return nil, err
		}
		if err := s.st.credit(FeeUID, m.Base, makerFee); err != nil {
			return nil, err
		}
	}
	tr.PriceStr, tr.QtyStr, tr.QuoteStr = price.String(m.Quote), qty.String(m.Base), quoteAmt.String(m.Quote)
	if err := s.st.insertTrade(tr); err != nil {
		return nil, err
	}
	return tr, nil
}

// CancelOrder 撤單（歸還未成交鎖定）。
func (s *Service) CancelOrder(uid string, id int64) (*Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, err := s.st.getOrder(id)
	if err != nil {
		return nil, err
	}
	if o.UID != uid {
		return nil, fmt.Errorf("exchange: 非本人訂單 %d", id)
	}
	m, err := ParseMarket(o.Market)
	if err != nil {
		return nil, err
	}
	ok, err := s.st.cancelOrder(id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("exchange: 訂單 %d 無法撤銷（已成交/已撤）", id)
	}
	// 歸還剩餘鎖定。
	rem := o.remaining()
	if rem.Cmp(Amount{}) > 0 {
		_ = s.st.unlock(uid, sideLockAsset(o.Side, m), filledLock(o.Side, rem, o.Price, m.Base.Decimals()))
	}
	s.book(m).remove(id)
	o.Status = StatusCanceled
	return o, nil
}

// OrderBook 返回訂單簿深度。
func (s *Service) OrderBook(m Market, levels int) (bids, asks []PriceLevel, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !m.Supported() {
		return nil, nil, fmt.Errorf("exchange: 不支持的交易對 %s", m)
	}
	bids, asks = s.book(m).Depth(levels)
	return bids, asks, nil
}

// Withdraw 從交易所帳戶扣減資產（提現至鏈上錢包；鏈上側另扣轉帳手續費）。
func (s *Service) Withdraw(uid string, a Asset, amt Amount) error {
	if uid == "" || uid == FeeUID || !a.Valid() || amt.Cmp(Amount{}) <= 0 {
		return fmt.Errorf("exchange: 提現參數非法")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.debit(uid, a, amt)
}

// DrainFee 結算手續費帳戶：把 fee 帳戶指定資產全額/部分轉出（供獎勵池挹注）。
func (s *Service) DrainFee(a Asset, amt Amount) error {
	if !a.Valid() || amt.Cmp(Amount{}) <= 0 {
		return fmt.Errorf("exchange: 結算參數非法")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.debit(FeeUID, a, amt)
}

// Orders 返回用戶訂單（可依市場過濾；含未成交與歷史）。
func (s *Service) Orders(uid, market string) ([]*Order, error) {
	if uid == "" {
		return nil, fmt.Errorf("exchange: uid 必填")
	}
	if market != "" {
		m, err := ParseMarket(market)
		if err != nil {
			return nil, err
		}
		market = m.String()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.ordersByUID(uid, market)
}

// Trades 返回最近成交。
func (s *Service) Trades(market string, limit int) ([]*Trade, error) {
	if _, err := ParseMarket(market); err != nil {
		return nil, err
	}
	return s.st.Trades(market, limit)
}

// Balances 返回帳戶餘額。
func (s *Service) Balances(uid string) ([]*Balance, error) {
	return s.st.Balances(uid)
}

// Deposit 充值（外部入金：wallet 橋接用）。
func (s *Service) Deposit(uid string, a Asset, amt Amount, _ string) error {
	if !a.Valid() || amt.Cmp(Amount{}) <= 0 {
		return fmt.Errorf("exchange: 充值參數非法")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.credit(uid, a, amt)
}

// mulPrice 計算 qty×price（結果為 quote 最小單位；除以 base 小數位完成單位換算）。
func mulPrice(qty, price Amount, baseDec int) Amount {
	num := new(big.Int).Mul(qty.ToBig(), price.ToBig())
	div := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(baseDec)), nil)
	return Amount{Big: new(big.Int).Div(num, div)}
}

// Klines 委派至 Store（K 線聚合）。
func (s *Service) Klines(market string, intervalSec, limit int) ([]Kline, error) {
	if market == "" {
		return nil, fmt.Errorf("exchange: market 必填")
	}
	m, err := ParseMarket(market)
	if err != nil {
		// M104：主流幣參考市場——以參考價生成展示 K 線。
		if _, ok := IsRefMarket(market); ok {
			return s.RefKlines(market, intervalSec, limit)
		}
		return nil, err
	}
	return s.st.Klines(m.String(), intervalSec, limit)
}

// MyTrades 委派至 Store（個人成交紀錄）。
// RecentTrades 最近成交（薄包 Store，供門戶行情）。
func (s *Service) RecentTrades(m Market, limit int) ([]Trade, error) {
	if !m.Supported() {
		return nil, fmt.Errorf("exchange: 不支持的交易對 %s", m)
	}
	return s.st.RecentTrades(m.String(), limit)
}

func (s *Service) MyTrades(uid, market string, limit int) ([]Trade, error) {
	if uid == "" {
		return nil, fmt.Errorf("exchange: uid 必填")
	}
	return s.st.MyTrades(uid, market, limit)
}
