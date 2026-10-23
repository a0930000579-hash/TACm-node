package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"strings"

	"tacm/internal/exchange"
	"tacm/internal/wallet"
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

// handleExchangeVerifyAddress POST /api/exchange/verify-address {uid?}
// v158：交易所「驗證地址」——確認操作帳戶＝登入會員綁定的鏈上錢包地址
// （與礦機/錢包同源 user.wallet_addr）。未登入回 need_login；uid 空時
// 自動回傳會員綁定地址供前端採用；uid 非空時校驗一致性。
func (s *RPCServer) handleExchangeVerifyAddress(w http.ResponseWriter, r *http.Request) {
	if s.node.authSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "auth service not enabled")
		return
	}
	u, err := s.node.authSvc.CurrentUser(r)
	if err != nil || u == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "need_login": true, "error": "login_required"})
		return
	}
	var req struct {
		UID string `json:"uid"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_json"})
		return
	}
	target := u.WalletAddr
	verified := true
	if req.UID != "" {
		verified = req.UID == target
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "verified": verified, "address": target, "logged_in": true,
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


// handleNativeSwap POST /api/exchange/native-swap — v97：TACm ⇄ TiUSD 兌換即發行。
// 用 TACm 直接兌 TiUSD（不需要 USDT、不需要訂單簿深度）：TACm 進兌換池做贖回儲備，
// TiUSD 在 1:1 供應上限內鏈上發行（比照 Curve 兌換所）；反向則從池存量兌出。
// 定價：TACM/TIUSD AMM 池 reserve 比（1 TACm = reserve1/reserve0 TiUSD），費 0.5% 入 fee 池。
func (s *RPCServer) handleNativeSwap(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Address string `json:"address"`
		From    string `json:"from"`
		To      string `json:"to"`
		Amount  string `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	req.Address = strings.TrimSpace(req.Address)
	fromA, ok1 := wallet.NormalizeAsset(req.From)
	toA, ok2 := wallet.NormalizeAsset(req.To)
	if !ok1 || !ok2 {
		writeErr(w, http.StatusBadRequest, "unsupported asset (must be TACm or TiUSD)")
		return
	}
	if !((fromA == wallet.AssetTACm && toA == wallet.AssetTiUSD) || (fromA == wallet.AssetTiUSD && toA == wallet.AssetTACm)) {
		writeErr(w, http.StatusBadRequest, "native-swap only supports TACm <-> TiUSD")
		return
	}
	amt, ok := new(big.Int).SetString(req.Amount, 10)
	if !ok || amt.Sign() <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid amount")
		return
	}
	// v99 定價：TiUSD 恆等 1 USDT（錨定承諾）。TACm 的美元價優先取交易所 TACM/USDT 真實成交價；
	// 市場未有交易（無成交）時按最低供應量參考價 0.330 計算（1 TACm = 0.330 USDT = 0.330 TiUSD）。
	price := 0.330
	if ex := s.node.Exchange(); ex != nil {
		mkt := exchange.Market{Base: exchange.AssetTACm, Quote: exchange.AssetUSDT}
		if trs, terr := ex.Trades(mkt.String(), 1); terr == nil && len(trs) > 0 {
			if p, perr := strconv.ParseFloat(trs[0].PriceStr, 64); perr == nil && p > 0 {
				price = p
			}
		}
	}
	if price <= 0 {
		writeErr(w, http.StatusServiceUnavailable, "市場價異常")
		return
	}
	// 費 0.5%（50bp），以 from 資產計。
	feePpm := int64(50)
	fee := new(big.Int).Mul(amt, big.NewInt(feePpm))
	fee.Div(fee, big.NewInt(10000))
	received := new(big.Int).Sub(amt, fee)
	ws := s.node.walletSvc
	if ws == nil {
		writeErr(w, http.StatusServiceUnavailable, "wallet service unavailable")
		return
	}
	if fromA == wallet.AssetTACm {
		// TACm→TiUSD：扣 TACm（進兌換池）＋TiUSD 上限內發行。
		fromAmt := wallet.Amount{I64: amt.Int64(), Big: amt}
		feeAmt := wallet.Amount{I64: fee.Int64(), Big: fee}
		if err := ws.InternalTransfer(req.Address, "stable-pool", wallet.AssetTACm, fromAmt, "native:swap:out"); err != nil {
			writeErr(w, http.StatusPaymentRequired, "insufficient TACm balance: "+err.Error())
			return
		}
		if fee.Sign() > 0 {
			if err := ws.InternalTransfer("stable-pool", "fee", wallet.AssetTACm, feeAmt, "native:swap:fee"); err != nil {
				_ = ws.InternalTransfer("stable-pool", req.Address, wallet.AssetTACm, fromAmt, "native:swap:refund")
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		// TiUSD 數量（micro）= TACm_units × price / 1e12（TACm 1e18→TiUSD 1e6）。
		priceInt := int64(math.Round(price * 1e6))
		if priceInt <= 0 {
			_ = ws.InternalTransfer("stable-pool", req.Address, wallet.AssetTACm, fromAmt, "native:swap:refund")
			writeErr(w, http.StatusServiceUnavailable, "TACM/USDT 池價異常")
			return
		}
		tiusdUnits := new(big.Int).Mul(received, big.NewInt(priceInt))
		tiusdUnits.Div(tiusdUnits, new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
		if tiusdUnits.Sign() <= 0 {
			_ = ws.InternalTransfer("stable-pool", req.Address, wallet.AssetTACm, fromAmt, "native:swap:refund")
			writeErr(w, http.StatusBadRequest, "兌換數量過小（低於 1 TiUSD）")
			return
		}
		if _, err := ws.MintTiUSD(req.Address, tiusdUnits.Int64(), "native:swap:in"); err != nil {
			_ = ws.InternalTransfer("stable-pool", req.Address, wallet.AssetTACm, fromAmt, "native:swap:refund")
			writeErr(w, http.StatusInternalServerError, "TiUSD 兌換發行受限："+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "from": req.Address, "from_asset": "TACm", "to_asset": "TIUSD",
			"amount": amt.String(), "fee": fee.String(), "received_tiusd": tiusdUnits.String(), "price": fmt.Sprintf("%.6f", price),
		})
		return
	}
	// TiUSD→TACm：扣 TiUSD（進兌換池）＋從池 TACm 存量兌出（不增發 TACm）。
	fromAmt := wallet.Amount{I64: amt.Int64(), Big: amt}
	feeAmt := wallet.Amount{I64: fee.Int64(), Big: fee}
	if err := ws.InternalTransfer(req.Address, "stable-pool", wallet.AssetTiUSD, fromAmt, "native:swap:out"); err != nil {
		writeErr(w, http.StatusPaymentRequired, "insufficient TIUSD balance: "+err.Error())
		return
	}
	if fee.Sign() > 0 {
		if err := ws.InternalTransfer("stable-pool", "fee", wallet.AssetTiUSD, feeAmt, "native:swap:fee"); err != nil {
			_ = ws.InternalTransfer("stable-pool", req.Address, wallet.AssetTiUSD, fromAmt, "native:swap:refund")
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	// TACm 數量 = TiUSD_micro × 1e12 / price → Mul(received, 1e18) / priceInt。
	priceInt := int64(math.Round(price * 1e6))
	if priceInt <= 0 {
		_ = ws.InternalTransfer("stable-pool", req.Address, wallet.AssetTiUSD, fromAmt, "native:swap:refund")
		writeErr(w, http.StatusServiceUnavailable, "TACM/USDT 池價異常")
		return
	}
	tacmUnits := new(big.Int).Mul(received, new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	tacmUnits.Div(tacmUnits, big.NewInt(priceInt))
	if tacmUnits.Sign() <= 0 {
		_ = ws.InternalTransfer("stable-pool", req.Address, wallet.AssetTiUSD, fromAmt, "native:swap:refund")
		writeErr(w, http.StatusBadRequest, "兌換數量過小（低於 1 micro-TACm）")
		return
	}
	poolAcc, err := ws.Balance("stable-pool")
	if err != nil || poolAcc.TACmBalance == nil || poolAcc.TACmBalance.Cmp(tacmUnits) < 0 {
		_ = ws.InternalTransfer("stable-pool", req.Address, wallet.AssetTiUSD, fromAmt, "native:swap:refund")
		writeErr(w, http.StatusPaymentRequired, "兌換池 TACm 存量不足（請先 TACm→TiUSD 充入兌換池）")
		return
	}
	tacmAmt := wallet.Amount{Big: tacmUnits}
	if err := ws.InternalTransfer("stable-pool", req.Address, wallet.AssetTACm, tacmAmt, "native:swap:in"); err != nil {
		_ = ws.InternalTransfer("stable-pool", req.Address, wallet.AssetTiUSD, fromAmt, "native:swap:refund")
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "from": req.Address, "from_asset": "TIUSD", "to_asset": "TACm",
		"amount": amt.String(), "fee": fee.String(), "received_tacm": tacmUnits.String(), "price": fmt.Sprintf("%.6f", price),
	})
}

// handleStableSwap POST /api/exchange/stable-swap — v78：穩定幣兌換（USDT ↔ TiUSD 1:1）。
// body: {address, from_asset, to_asset, amount}。費率 0.5%（50bp）入 fee 池。
// 錨定機制：扣 from 資產（含費）＋發行 to 資產（1:1 peg）——TiUSD/USDT 互相兌換，
// 任一方向皆有明確供應/贖回通道，穩定幣流通閉環成立（比照 Curve 穩定幣兌換所）。
func (s *RPCServer) handleStableSwap(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Address   string `json:"address"`
		FromAsset string `json:"from_asset"`
		ToAsset   string `json:"to_asset"`
		Amount    string `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	fromA, ok1 := wallet.NormalizeAsset(req.FromAsset)
	toA, ok2 := wallet.NormalizeAsset(req.ToAsset)
	if !ok1 || !ok2 {
		writeErr(w, http.StatusBadRequest, "unsupported asset (must be TiUSD or USDT)")
		return
	}
	if !((fromA == wallet.AssetTiUSD && toA == wallet.AssetUSDT) || (fromA == wallet.AssetUSDT && toA == wallet.AssetTiUSD)) {
		writeErr(w, http.StatusBadRequest, "stable-swap only supports TiUSD <-> USDT")
		return
	}
	amt, ok := new(big.Int).SetString(req.Amount, 10)
	if !ok || amt.Sign() <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid amount")
		return
	}
	ws := s.node.walletSvc
	if ws == nil {
		writeErr(w, http.StatusServiceUnavailable, "wallet service unavailable")
		return
	}
	// 0.5% 費（50bp），費以 from 資產計。
	fee := new(big.Int).Mul(amt, big.NewInt(50))
	fee.Div(fee, big.NewInt(10000))
	if fee.Sign() == 0 {
		fee.SetInt64(0)
	}
	received := new(big.Int).Sub(amt, fee)
	fromAmt := wallet.Amount{I64: amt.Int64(), Big: amt}
	feeAmt := wallet.Amount{I64: fee.Int64(), Big: fee}
	recvAmt := wallet.Amount{I64: received.Int64(), Big: received}
	// v99 儲備檢查（必須在扣款前）：無真實 USDT 儲備時禁止 USDT→TiUSD——
	// 池 USDT 餘額為 0 表示無儲備背書，不得以 USDT 兌出 TiUSD；TiUSD 僅能由 TACm 閃兌（native-swap）產生。
	if fromA == wallet.AssetUSDT && toA == wallet.AssetTiUSD {
		poolAcc, err := ws.Balance("stable-pool")
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "兌換池不可用："+err.Error())
			return
		}
		if poolAcc.USDTBalance <= 0 {
			writeErr(w, http.StatusPaymentRequired, "兌換池無真實 USDT 儲備，禁止 USDT→TiUSD 兌換；請用 TACm 閃兌 TiUSD（native-swap）")
			return
		}
	}
	// 1) 扣 from 資產（含費）。
	if err := ws.InternalTransfer(req.Address, "stable-pool", fromA, fromAmt, "stable:swap:out"); err != nil {
		writeErr(w, http.StatusPaymentRequired, "insufficient "+string(fromA)+" balance: "+err.Error())
		return
	}
	// 2) 費入 fee 池（from 資產）。
	if fee.Sign() > 0 {
		if err := ws.InternalTransfer("stable-pool", "fee", fromA, feeAmt, "stable:swap:fee"); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	// 3) 兌換入帳（v96：供應治理，比照 Curve 兌換所）
	//    USDT→TiUSD：走 MintTiUSD（受 1:1 供應上限治理），USDT 進池為贖回儲備；
	//    TiUSD→USDT：從 stable-pool 存量兌出（不增發 USDT），池不足則退款。
	if fromA == wallet.AssetUSDT && toA == wallet.AssetTiUSD {
		if _, err := ws.MintTiUSD(req.Address, recvAmt.I64, "stable:swap:in"); err != nil {
			_ = ws.InternalTransfer("stable-pool", req.Address, wallet.AssetUSDT, fromAmt, "stable:swap:refund")
			writeErr(w, http.StatusInternalServerError, "TiUSD 兌換發行受限："+err.Error())
			return
		}
	} else {
		poolAcc, err := ws.Balance("stable-pool")
		if err != nil {
			_ = ws.InternalTransfer("stable-pool", req.Address, wallet.AssetTiUSD, fromAmt, "stable:swap:refund")
			writeErr(w, http.StatusInternalServerError, "兌換池不可用："+err.Error())
			return
		}
		if poolAcc.USDTBalance < recvAmt.I64 {
			_ = ws.InternalTransfer("stable-pool", req.Address, wallet.AssetTiUSD, fromAmt, "stable:swap:refund")
			writeErr(w, http.StatusPaymentRequired, "兌換池 USDT 存量不足（可用 "+wallet.FormatAmountI64(poolAcc.USDTBalance, wallet.AssetUSDT)+"），請先 USDT→TiUSD 充入兌換池")
			return
		}
		if err := ws.InternalTransfer("stable-pool", req.Address, wallet.AssetUSDT, recvAmt, "stable:swap:in"); err != nil {
			_ = ws.InternalTransfer("stable-pool", req.Address, wallet.AssetTiUSD, fromAmt, "stable:swap:refund")
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "from": req.Address,
		"from_asset": string(fromA), "to_asset": string(toA),
		"amount": amt.String(), "fee": fee.String(), "received": received.String(),
	})
}

// handleStableInfo GET /api/exchange/stable-info — v78：穩定幣兌換資訊（匯率/供應/費率）。
func (s *RPCServer) handleStableInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "rate": "1", "fee_bps": 50,
		"note": "TiUSD and USDT swap 1:1 (0.5% fee to treasury)",
		"supported": []string{"TiUSD", "USDT"},
	})
}

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
	// v79：估値兌換（非鏈上資產）已停用——只允許鏈上流通資產（TACM/TiUSD/USDT）真實撮合交易。
	writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "reference markets disabled: only on-chain assets (TACM/TiUSD/USDT) are tradable"})
	return
}
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
