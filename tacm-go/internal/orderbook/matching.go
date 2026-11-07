// matching.go — 確定性價格-時間優先撮合引擎（純狀態側）。
//
// 流程：PlaceOrder 驗證 → 寫入新單 → 立即對 opposite side 掛單逐筆撮合
// （buy taker 吃最低賣單；sell taker 吃最高買單；同價以 id 小者優先）。
// 成交價 = resting maker 掛單價；成交量 = min(taker 剩餘, maker 剩餘)。
// 每筆成交寫入 ob_trades，更新雙方 filled/status，累計 taker fee。
//
// 本包不做金錢移轉；回傳 Fill 清單讓 node RPC 層在 escrow / 用戶 / fee 間結算。

package orderbook

import (
	"fmt"
	"math"
)

// round4 四捨五入至 4 位小數，避免 float 尾差。
func round4(v float64) float64 { return math.Round(v*10000) / 10000 }

// PlaceOrder 下一張限價單並立即撮合。
//   - maker：正規化後地址（tx0）
//   - side：buy | sell；type 固定 limit
//   - price：quote per base；amount：base
//
// 回傳 PlaceResult（含新單、逐筆成交、仍掛簿的剩餘量）。
// 錯誤：市場不存在、side/type 非法、price/amount <= 0、未達最小掛單量。
func (s *Store) PlaceOrder(maker, market, side, typ string, price, amount float64) (*PlaceResult, error) {
	maker = trim(maker)
	if maker == "" {
		return nil, fmt.Errorf("orderbook: 缺少 maker 地址")
	}
	if side != SideBuy && side != SideSell {
		return nil, fmt.Errorf("orderbook: side 必須為 buy/sell (got %q)", side)
	}
	if typ == "" {
		typ = TypeLimit
	}
	if typ != TypeLimit {
		return nil, fmt.Errorf("orderbook: 僅支援 limit 單 (got %q)", typ)
	}
	if price <= 0 {
		return nil, fmt.Errorf("orderbook: price 必須 > 0")
	}
	if amount <= 0 {
		return nil, fmt.Errorf("orderbook: amount 必須 > 0")
	}
	norm, err := s.NormalizeMarket(market)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	mkt, err := s.marketLocked(norm)
	if err != nil {
		return nil, err
	}
	if amount < mkt.MinAmount {
		return nil, fmt.Errorf("orderbook: amount %v < 最小掛單量 %v", amount, mkt.MinAmount)
	}
	if mkt.PriceTick > 0 {
		// 對齊 price tick。
		ticks := math.Round(price / mkt.PriceTick)
		price = round4(ticks * mkt.PriceTick)
		if price <= 0 {
			return nil, fmt.Errorf("orderbook: price 對齊 tick 後為 0")
		}
	}

	now := nowSec()
	// 1) 寫入新單（初始 status=open, filled=0）。
	res, err := s.db.Exec(`INSERT INTO ob_orders(market,maker,side,type,price,amount,filled,fee,status,created_at,updated_at)
		VALUES(?,?,?,?,?,?,0,0,?, ?, ?)`,
		norm, maker, side, typ, price, amount, StatusOpen, now, now)
	if err != nil {
		return nil, fmt.Errorf("orderbook: 寫入掛單: %w", err)
	}
	takerID, _ := res.LastInsertId()
	takerOrder, err := s.getOrderLocked(takerID)
	if err != nil {
		return nil, err
	}

	// 2) 撮合 opposite side。
	fills, err := s.matchLocked(takerOrder)
	if err != nil {
		return nil, err
	}

	// 3) 重新讀 taker order 得到最終狀態。
	final, err := s.getOrderLocked(takerID)
	if err != nil {
		return nil, err
	}
	return &PlaceResult{
		Order:     final,
		Fills:     fills,
		Remaining: round4(final.Remaining()),
	}, nil
}

// matchLocked 對 takerOrder 執行撮合。呼叫方須持鎖。
func (s *Store) matchLocked(taker *Order) ([]Fill, error) {
	var (
		fills     []Fill
		opposite  string
		priceCond string // SQL WHERE 條件
		orderBy   string
	)
	if taker.Side == SideBuy {
		// buy taker 吃賣單：賣單價 <= 買單價；從最低價開始（price ASC, id ASC）。
		opposite = SideSell
		priceCond = "price <= ?"
		orderBy = "price ASC, id ASC"
	} else {
		// sell taker 吃買單：買單價 >= 賣單價；從最高價開始（price DESC, id ASC）。
		opposite = SideBuy
		priceCond = "price >= ?"
		orderBy = "price DESC, id ASC"
	}

	makerRate, takerRate := s.makerRate, s.takerRate
	remaining := taker.Remaining()

	for remaining > 1e-9 {
		rows, err := s.db.Query(`SELECT `+orderCols+` FROM ob_orders
			WHERE market=? AND side=? AND status IN (?,?) AND `+priceCond+`
			ORDER BY `+orderBy+` LIMIT 1`,
			taker.Market, opposite, StatusOpen, StatusPartial, taker.Price)
		if err != nil {
			return nil, fmt.Errorf("orderbook: 查詢對手掛單: %w", err)
		}
		makerOrder, err := scanOne(rows)
		rows.Close()
		if err != nil {
			if err == errNoRow {
				break // 無對手單可撮合
			}
			return nil, err
		}
		// 撮合成交量。
		qty := math.Min(remaining, makerOrder.Remaining())
		qty = round4(qty)
		if qty <= 1e-9 {
			break
		}
		fillPrice := makerOrder.Price // 以 resting maker 價成交
		quoteBase := round4(fillPrice * qty)
		takerFee := round4(quoteBase * takerRate)
		makerFee := round4(quoteBase * makerRate)
		now := nowSec()

		// 更新 taker filled/fee/status。
		taker.Filled = round4(taker.Filled + qty)
		taker.Fee = round4(taker.Fee + takerFee)
		taker.Status = statusOf(taker.Filled, taker.Amount)
		if _, err := s.db.Exec(`UPDATE ob_orders SET filled=?, fee=?, status=?, updated_at=? WHERE id=?`,
			taker.Filled, taker.Fee, taker.Status, now, taker.ID); err != nil {
			return nil, fmt.Errorf("orderbook: 更新 taker 單: %w", err)
		}

		// 更新 maker filled/status（maker fee 不記入訂單 fee 欄；只記 trade）。
		makerOrder.Filled = round4(makerOrder.Filled + qty)
		makerOrder.Status = statusOf(makerOrder.Filled, makerOrder.Amount)
		if _, err := s.db.Exec(`UPDATE ob_orders SET filled=?, status=?, updated_at=? WHERE id=?`,
			makerOrder.Filled, makerOrder.Status, now, makerOrder.ID); err != nil {
			return nil, fmt.Errorf("orderbook: 更新 maker 單: %w", err)
		}

		// 寫成交。
		res, err := s.db.Exec(`INSERT INTO ob_trades(market,taker,maker,taker_order_id,maker_order_id,
			taker_side,price,amount,taker_fee,maker_fee,time) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			taker.Market, taker.Maker, makerOrder.Maker, taker.ID, makerOrder.ID,
			taker.Side, fillPrice, qty, takerFee, makerFee, now)
		if err != nil {
			return nil, fmt.Errorf("orderbook: 寫成交: %w", err)
		}
		tradeID, _ := res.LastInsertId()
		fills = append(fills, Fill{
			ID: tradeID, Market: taker.Market,
			Taker: taker.Maker, Maker: makerOrder.Maker,
			TakerOrderID: taker.ID, MakerOrderID: makerOrder.ID,
			TakerSide: taker.Side, Price: fillPrice, Amount: qty,
			TakerFee: takerFee, MakerFee: makerFee, Time: now,
		})

		remaining = round4(taker.Amount - taker.Filled)
	}
	return fills, nil
}

// errNoRow 內部：查無列。
var errNoRow = fmt.Errorf("no row")

// scanOne 把 rows 的第一列掃成 *Order；無列回 errNoRow。
func scanOne(rows interface {
	Next() bool
	Scan(...any) error
}) (*Order, error) {
	if !rows.Next() {
		return nil, errNoRow
	}
	return scanOrder(rows)
}

// statusOf 依 filled/amount 推 status。
func statusOf(filled, amount float64) string {
	rem := round4(amount - filled)
	if rem <= 1e-9 {
		return StatusFilled
	}
	if filled <= 1e-9 {
		return StatusOpen
	}
	return StatusPartial
}

// CancelResult 撤單回執。
type CancelResult struct {
	Order     *Order  `json:"order"`
	Remaining float64 `json:"remaining"` // 未成交 base 量
}

// CancelOrder 撤銷一張 open/partial 掛單。
//   - caller：呼叫者地址（必須等於 order.maker，否則回錯）
//
// 回傳 CancelResult（含訂單與剩餘量）；實際退還金額由 node RPC 依 side/price/takerRate 計算。
func (s *Store) CancelOrder(id int64, caller string) (*CancelResult, error) {
	caller = trim(caller)
	s.mu.Lock()
	defer s.mu.Unlock()
	o, err := s.getOrderLocked(id)
	if err != nil {
		return nil, err
	}
	if caller == "" || caller != o.Maker {
		return nil, fmt.Errorf("orderbook: 訂單非本人 (order.maker=%s caller=%s)", o.Maker, caller)
	}
	if o.Status == StatusFilled || o.Status == StatusCanceled {
		return nil, fmt.Errorf("orderbook: 訂單狀態 %s 不可撤銷", o.Status)
	}
	remaining := round4(o.Remaining())
	if _, err := s.db.Exec(`UPDATE ob_orders SET status=?, updated_at=? WHERE id=?`,
		StatusCanceled, nowSec(), id); err != nil {
		return nil, fmt.Errorf("orderbook: 更新撤單: %w", err)
	}
	o.Status = StatusCanceled
	return &CancelResult{Order: o, Remaining: remaining}, nil
}

// Depth 回傳某市場聚合深度（bids 高→低；asks 低→高；每側最多 n 檔）。
func (s *Store) Depth(market string, n int) (*Depth, error) {
	if n <= 0 || n > 100 {
		n = 20
	}
	norm, err := s.NormalizeMarket(market)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.marketLocked(norm); err != nil {
		return nil, err
	}

	d := &Depth{Market: norm, Bids: []DepthLevel{}, Asks: []DepthLevel{}}

	// Bids：買單，價格降序聚合。
	rows, err := s.db.Query(`SELECT price, amount-filled FROM ob_orders
		WHERE market=? AND side=? AND status IN (?,?) AND (amount-filled)>1e-9
		ORDER BY price DESC, id ASC`, norm, SideBuy, StatusOpen, StatusPartial)
	if err != nil {
		return nil, err
	}
	d.Bids = aggregateLevels(rows, n)
	rows.Close()

	// Asks：賣單，價格升序聚合。
	rows2, err := s.db.Query(`SELECT price, amount-filled FROM ob_orders
		WHERE market=? AND side=? AND status IN (?,?) AND (amount-filled)>1e-9
		ORDER BY price ASC, id ASC`, norm, SideSell, StatusOpen, StatusPartial)
	if err != nil {
		return nil, err
	}
	d.Asks = aggregateLevels(rows2, n)
	rows2.Close()
	return d, nil
}

// aggregateLevels 把 (price, remaining) 行聚合成 n 檔。
func aggregateLevels(rows rowIter, n int) []DepthLevel {
	out := []DepthLevel{}
	for rows.Next() {
		var price, remaining float64
		if err := rows.Scan(&price, &remaining); err != nil {
			break
		}
		if remaining <= 1e-9 {
			continue
		}
		if len(out) == 0 || out[len(out)-1].Price != price {
			out = append(out, DepthLevel{Price: price, Amount: round4(remaining), OrderCount: 1})
		} else {
			out[len(out)-1].Amount = round4(out[len(out)-1].Amount + remaining)
			out[len(out)-1].OrderCount++
		}
		if len(out) >= n {
			break
		}
	}
	return out
}

// rowIter 是 *sql.Rows 的最小介面（方便聚合函式接受）。
type rowIter interface {
	Next() bool
	Scan(...any) error
}

func trim(s string) string {
	// 局部 strings.TrimSpace 以避免多 import。
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
