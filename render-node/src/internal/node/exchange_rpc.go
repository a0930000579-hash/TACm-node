package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"tacm/internal/exchange"
)

// Exchange 返回節點的交易所撮合引擎。
func (n *Node) Exchange() *exchange.Service { return n.exchangeSvc }

// ExchangeMarkets 返回交易所行情快照（WebSocket market_update 推送數據源，M114）。
func (n *Node) ExchangeMarkets() ([]exchange.MarketQuote, error) {
	if n.exchangeSvc == nil {
		return nil, errors.New("exchange_disabled")
	}
	return n.exchangeSvc.MarketsOverview()
}

// ExchangeOrderBook 返回指定市場訂單簿（M115：WebSocket depth 推送數據源）。
func (n *Node) ExchangeOrderBook(market string, depth int) (bids, asks []exchange.PriceLevel, err error) {
	if n.exchangeSvc == nil {
		return nil, nil, errors.New("exchange_disabled")
	}
	m, perr := exchange.ParseMarket(market)
	if perr != nil {
		return nil, nil, perr
	}
	return n.exchangeSvc.OrderBook(m, depth)
}

// ExchangeRecentTrades 返回指定市場最新成交（M115：WebSocket trades 推送數據源）。
func (n *Node) ExchangeRecentTrades(market string, limit int) ([]exchange.Trade, error) {
	if n.exchangeSvc == nil {
		return nil, errors.New("exchange_disabled")
	}
	m, perr := exchange.ParseMarket(market)
	if perr != nil {
		return nil, perr
	}
	return n.exchangeSvc.RecentTrades(m, limit)
}

// handleExchangeBalances GET /api/exchange/balances?uid=xxx
func (s *RPCServer) handleExchangeBalances(w http.ResponseWriter, r *http.Request) {
	if s.node.exchangeSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "exchange_disabled"})
		return
	}
	uid := r.URL.Query().Get("uid")
	if uid == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "uid_required"})
		return
	}
	bal, err := s.node.exchangeSvc.Balances(uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	out := []map[string]any{}
	for _, b := range bal {
		out = append(out, map[string]any{
			"asset":   string(b.Asset),
			"avail":   b.AvailStr,
			"locked":  b.LockedStr,
			"avail_r": b.Avail.String(b.Asset),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "balances": out})
}

// handleExchangeDeposit POST /api/exchange/deposit {uid,asset,amount}
func (s *RPCServer) handleExchangeDeposit(w http.ResponseWriter, r *http.Request) {
	if s.node.exchangeSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "exchange_disabled"})
		return
	}
	var req struct {
		UID    string `json:"uid"`
		Asset  string `json:"asset"`
		Amount string `json:"amount"`
		Memo   string `json:"memo"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_json"})
		return
	}
	a := exchange.Asset(req.Asset)
	if err := s.node.exchangeSvc.Deposit(req.UID, a, mustAmt(req.Amount, a), req.Memo); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "uid": req.UID, "asset": req.Asset, "amount": req.Amount})
}

// handleExchangeBook GET /api/exchange/book?market=TACM/USDT&depth=10
func (s *RPCServer) handleExchangeBook(w http.ResponseWriter, r *http.Request) {
	if s.node.exchangeSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "exchange_disabled"})
		return
	}
	m, err := exchange.ParseMarket(r.URL.Query().Get("market"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	depth := 10
	if d := r.URL.Query().Get("depth"); d != "" {
		if v, e := strconv.Atoi(d); e == nil {
			depth = v
		}
	}
	bids, asks, err := s.node.exchangeSvc.OrderBook(m, depth)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	trades, terr := s.node.exchangeSvc.RecentTrades(m, 8)
	if terr != nil {
		trades = nil
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "market": m.String(),
		"bids": bids, "asks": asks,
		"recent_trades": trades,
	})
}

// handleExchangeOrder POST /api/exchange/order {uid,market,side,type,price,qty}
func (s *RPCServer) handleExchangeOrder(w http.ResponseWriter, r *http.Request) {
	if s.node.exchangeSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "exchange_disabled"})
		return
	}
	var req struct {
		UID    string `json:"uid"`
		Market string `json:"market"`
		Side   string `json:"side"`
		Type   string `json:"type"`
		Price  string `json:"price"`
		Qty    string `json:"qty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_json"})
		return
	}
	m, err := exchange.ParseMarket(req.Market)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	price, err := exchange.NewAmount(req.Price, m.Quote)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_price: " + err.Error()})
		return
	}
	qty, err := exchange.NewAmount(req.Qty, m.Base)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_qty: " + err.Error()})
		return
	}
	res, err := s.node.exchangeSvc.PlaceOrder(req.UID, m, exchange.Side(req.Side), exchange.OrderType(req.Type), price, qty)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	trades := []map[string]any{}
	for _, t := range res.Trades {
		trades = append(trades, map[string]any{
			"price": t.PriceStr, "qty": t.QtyStr, "quote": t.QuoteStr,
			"taker_fee": t.TakerFee.String(m.Quote), "maker_fee": t.MakerFee.String(m.Quote),
			"taker": t.TakerUID, "maker": t.MakerUID,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "order_id": res.Order.ID, "status": string(res.Order.Status),
		"filled_qty": res.FilledQtyStr, "trades": trades,
	})
}

// handleExchangeOrders GET /api/exchange/orders?uid=xxx&market=TACM/USDT
func (s *RPCServer) handleExchangeOrders(w http.ResponseWriter, r *http.Request) {
	if s.node.exchangeSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "exchange_disabled"})
		return
	}
	uid := r.URL.Query().Get("uid")
	orders, err := s.node.exchangeSvc.Orders(uid, r.URL.Query().Get("market"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	out := []map[string]any{}
	for _, o := range orders {
		out = append(out, map[string]any{
			"id": o.ID, "uid": o.UID, "market": o.Market, "side": string(o.Side),
			"type": string(o.Type), "price": o.Price.String(o.QuoteQuote),
			"qty": o.Qty.String(o.QuoteBase), "filled": o.Filled.String(o.QuoteBase),
			"status": string(o.Status), "ts": o.CreatedTs,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "orders": out})
}

// handleExchangeCancel POST /api/exchange/cancel {uid,order_id}
func (s *RPCServer) handleExchangeCancel(w http.ResponseWriter, r *http.Request) {
	if s.node.exchangeSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "exchange_disabled"})
		return
	}
	var req struct {
		UID     string `json:"uid"`
		OrderID int64  `json:"order_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_json"})
		return
	}
	o, err := s.node.exchangeSvc.CancelOrder(req.UID, req.OrderID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "order_id": o.ID, "status": string(o.Status)})
}

// handleExchangeFlash POST /api/exchange/flash {uid,from,to,amount}
func (s *RPCServer) handleExchangeFlash(w http.ResponseWriter, r *http.Request) {
	if s.node.exchangeSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "exchange_disabled"})
		return
	}
	var req struct {
		UID    string `json:"uid"`
		From   string `json:"from"`
		To     string `json:"to"`
		Amount string `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_json"})
		return
	}
	res, err := s.node.exchangeSvc.FlashSwap(req.UID, exchange.Asset(req.From), exchange.Asset(req.To), mustAmt(req.Amount, exchange.Asset(req.From)))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "from": res.FromStr, "to": res.ToStr, "fee": res.FeeStr, "price": res.PriceStr,
	})
}

// handleExchangeBotStart POST /api/exchange/bot/start
func (s *RPCServer) handleExchangeBotStart(w http.ResponseWriter, r *http.Request) {
	if s.node.exchangeSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "exchange_disabled"})
		return
	}
	var req exchange.BotConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_json"})
		return
	}
	bot, err := s.node.exchangeSvc.StartBot(req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bot_id": bot.ID, "status": bot.Status})
}

// handleExchangeBotStop POST /api/exchange/bot/stop {id}
func (s *RPCServer) handleExchangeBotStop(w http.ResponseWriter, r *http.Request) {
	if s.node.exchangeSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "exchange_disabled"})
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_json"})
		return
	}
	st, err := s.node.exchangeSvc.StopBot(req.ID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bot": st})
}

// handleExchangeBots GET /api/exchange/bots
func (s *RPCServer) handleExchangeBots(w http.ResponseWriter, r *http.Request) {
	if s.node.exchangeSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "exchange_disabled"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bots": s.node.exchangeSvc.ListBots()})
}

// mustAmt 解析金額字串；非法時回傳 0（呼叫端以錯誤處理為準）。
func mustAmt(s string, a exchange.Asset) exchange.Amount {
	v, err := exchange.NewAmount(s, a)
	if err != nil {
		return exchange.Amount{}
	}
	return v
}

var _ = fmt.Sprintf // 保留 fmt 於本檔案（後續診斷使用）


// handleExchangeKlines GET /api/exchange/klines?market=TACM/USDT&interval=60&limit=200
func (s *RPCServer) handleExchangeKlines(w http.ResponseWriter, r *http.Request) {
	if s.node.exchangeSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "exchange_disabled"})
		return
	}
	interval := 60
	if v := r.URL.Query().Get("interval"); v != "" {
		if n, e := strconv.Atoi(v); e == nil {
			interval = n
		}
	}
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, e := strconv.Atoi(v); e == nil {
			limit = n
		}
	}
	kl, err := s.node.exchangeSvc.Klines(r.URL.Query().Get("market"), interval, limit)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "interval": interval, "klines": kl})
}

// handleExchangeMyTrades GET /api/exchange/mytrades?uid=&market=&limit=
func (s *RPCServer) handleExchangeMyTrades(w http.ResponseWriter, r *http.Request) {
	if s.node.exchangeSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "exchange_disabled"})
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, e := strconv.Atoi(v); e == nil {
			limit = n
		}
	}
	tr, err := s.node.exchangeSvc.MyTrades(r.URL.Query().Get("uid"), r.URL.Query().Get("market"), limit)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "trades": tr})
}

// handleExchangeMarkets GET /api/exchange/markets —— M104：交易所首頁市場列表
// （鏈上三市場真實行情＋主流幣五市場參考行情）。
func (s *RPCServer) handleExchangeMarkets(w http.ResponseWriter, r *http.Request) {
	if s.node.exchangeSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "exchange_disabled"})
		return
	}
	mk, err := s.node.exchangeSvc.MarketsOverview()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	src, upd := exchange.LiveSource()
	resp := map[string]any{"ok": true, "markets": mk, "source": src}
	if !upd.IsZero() {
		resp["updated_at"] = upd.Unix()
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleExchangeRefTrade POST /api/exchange/ref/trade {uid,market,side,qty}
// —— M104：主流幣估值兌換（買：USDT→主流幣；賣：主流幣→USDT；0.5% 費）。
func (s *RPCServer) handleExchangeRefTrade(w http.ResponseWriter, r *http.Request) {
	if s.node.exchangeSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "exchange_disabled"})
		return
	}
	var req struct {
		UID    string  `json:"uid"`
		Market string  `json:"market"`
		Side   string  `json:"side"`
		Qty    float64 `json:"qty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_json"})
		return
	}
	tr, err := s.node.exchangeSvc.RefTrade(req.UID, req.Market, req.Side, req.Qty)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "trade": map[string]any{
		"market": tr.Market, "side": tr.TakerSide, "price": tr.PriceStr,
		"qty": tr.QtyStr, "quote": tr.QuoteStr, "ts": tr.Ts,
	}})
}

// handleExchangeRefPositions GET /api/exchange/ref/positions?uid=
// —— M104：主流幣估值持倉（BTC/ETH/BNB/SOL/ADA＋USDT 可用）。
func (s *RPCServer) handleExchangeRefPositions(w http.ResponseWriter, r *http.Request) {
	if s.node.exchangeSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "exchange_disabled"})
		return
	}
	pos, err := s.node.exchangeSvc.RefPositions(r.URL.Query().Get("uid"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	out := []map[string]any{}
	for _, b := range pos {
		out = append(out, map[string]any{"asset": b.Asset, "avail": b.AvailStr, "locked": b.LockedStr})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "positions": out})
}
