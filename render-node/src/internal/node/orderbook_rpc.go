package node

// orderbook_rpc.go — 鏈上訂單簿式現貨交易 RPC：
//   POST /api/orderbook/place      — 掛限價單（自動撮合；未成交部分掛簿）
//   POST /api/orderbook/cancel    — 撤單（解鎖退還）
//   GET  /api/orderbook/depth      — 訂單簿深度（?market=TACm/TiUSD）
//   GET  /api/orderbook/trades     — 最近成交（?market=TACm/TiUSD）
//   GET  /api/orderbook/markets    — 市場列表
//   GET  /api/orderbook/myorders  — 我的委託（?address=0x...&market=）
//
// 資金流轉（全部走 walletSvc.InternalTransfer，不收鏈上手續費）：
//   - 掛單：user → orderbook_escrow 鎖定委託額。
//     買單鎖 quote = price*amount*(1+takerRate)（含 taker fee 緩衝）；
//     賣單鎖 base = amount。
//   - 成交：escrow → maker/taker/fee 三方結算。
//     買 taker：maker 得 quoteBase、fee 得 quoteBase*takerRate、taker 得 base。
//     賣 taker：maker 得 base、taker 得 quoteBase*(1-takerRate)、fee 得 quoteBase*takerRate。
//   - 撤單：escrow → user 退還未成交部分。
// 純狀態與撮合規則在 internal/orderbook；此檔只做金錢結算。

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"tacm/internal/orderbook"
	"tacm/internal/wallet"
)

// obAssets 依市場符號回傳 base/quote 對應的 wallet.Asset（大小寫不敏感解析）。
func obAssets(market string) (base, quote wallet.Asset, err error) {
	parts := strings.SplitN(market, "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("市場格式錯誤")
	}
	base, okBase := wallet.NormalizeAsset(parts[0])
	quote, okQuote := wallet.NormalizeAsset(parts[1])
	if !okBase || !okQuote {
		return "", "", fmt.Errorf("不支援的資產")
	}
	return base, quote, nil
}

// handleOrderBookPlace POST /api/orderbook/place
func (s *RPCServer) handleOrderBookPlace(w http.ResponseWriter, r *http.Request) {
	if s.node.obSt == nil || s.node.walletSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "OrderBook 未啟用")
		return
	}
	var req struct {
		Address string  `json:"address"`
		Market  string  `json:"market"`
		Side    string  `json:"side"`
		Type    string  `json:"type"`
		Price   float64 `json:"price"`
		Amount  float64 `json:"amount"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	maker, err := s.node.normalizeEthAddr(req.Address)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "address 無效")
		return
	}
	// 正規化市場（大小寫不敏感）。
	normMarket, err := s.node.obSt.NormalizeMarket(req.Market)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	baseAsset, quoteAsset, err := obAssets(normMarket)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	_, takerRate := s.node.obSt.FeeRates()

	// 1) 鎖定委託額：buy 鎖 quote（price*amount*(1+takerRate)），sell 鎖 base（amount）。
	var lockAmt float64
	var lockAsset wallet.Asset
	if req.Side == orderbook.SideBuy {
		lockAsset = quoteAsset
		lockAmt = req.Price * req.Amount * (1 + takerRate)
	} else {
		lockAsset = baseAsset
		lockAmt = req.Amount
	}
	if lockAmt <= 0 {
		writeErr(w, http.StatusBadRequest, "鎖定金額為 0")
		return
	}
	if err := s.node.walletSvc.InternalTransfer(maker, orderbook.EscrowAccount, lockAsset,
		wallet.NewAmountF(lockAmt, lockAsset), "orderbook:lock"); err != nil {
		writeErr(w, http.StatusBadRequest, "鎖定委託額失敗（餘額不足？）: "+err.Error())
		return
	}

	// 2) 呼叫純狀態撮合。
	res, err := s.node.obSt.PlaceOrder(maker, normMarket, req.Side, req.Type, req.Price, req.Amount)
	if err != nil {
		// 失敗：退回鎖定金額。
		_ = s.node.walletSvc.InternalTransfer(orderbook.EscrowAccount, maker, lockAsset,
			wallet.NewAmountF(lockAmt, lockAsset), "orderbook:refund-lock")
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	// 3) 逐筆成交結算。
	for _, f := range res.Fills {
		quoteBase := f.Price * f.Amount // quote 總額
		if f.TakerSide == orderbook.SideBuy {
			// Taker 是買方，Maker 是賣方（resting sell）。
			// Maker 得 quoteBase（full，makerFee=0）。
			// Fee 得 quoteBase*takerRate。
			// Taker 得 base。
			feeAmt := quoteBase * takerRate
			// Base 從 escrow 給 taker。
			if err := s.node.walletSvc.InternalTransfer(orderbook.EscrowAccount, f.Taker, baseAsset,
				wallet.NewAmountF(f.Amount, baseAsset), "ob:buy-base"); err != nil {
				writeErr(w, http.StatusInternalServerError, "結算 base 轉移失敗: "+err.Error())
				return
			}
			// Quote proceeds 從 escrow 給 maker。
			if quoteBase > 0 {
				if err := s.node.walletSvc.InternalTransfer(orderbook.EscrowAccount, f.Maker, quoteAsset,
					wallet.NewAmountF(quoteBase, quoteAsset), "ob:buy-maker-proceeds"); err != nil {
					writeErr(w, http.StatusInternalServerError, "結算 maker proceeds 失敗: "+err.Error())
					return
				}
			}
			// Fee 入 fee account。
			if feeAmt > 0 {
				_ = s.node.walletSvc.InternalTransfer(orderbook.EscrowAccount, orderbook.FeeAccount, quoteAsset,
					wallet.NewAmountF(feeAmt, quoteAsset), "ob:buy-fee")
			}
		} else {
			// Taker 是賣方，Maker 是買方（resting buy）。
			// Taker 得 quoteBase*(1-takerRate)。
			// Fee 得 quoteBase*takerRate。
			// Maker 得 base。
			feeAmt := quoteBase * takerRate
			takerProceeds := quoteBase - feeAmt
			// Base 從 escrow 給 maker。
			if err := s.node.walletSvc.InternalTransfer(orderbook.EscrowAccount, f.Maker, baseAsset,
				wallet.NewAmountF(f.Amount, baseAsset), "ob:sell-maker-base"); err != nil {
				writeErr(w, http.StatusInternalServerError, "結算 base 轉移失敗: "+err.Error())
				return
			}
			// Quote proceeds 從 escrow 給 taker。
			if takerProceeds > 0 {
				if err := s.node.walletSvc.InternalTransfer(orderbook.EscrowAccount, f.Taker, quoteAsset,
					wallet.NewAmountF(takerProceeds, quoteAsset), "ob:sell-taker-proceeds"); err != nil {
					writeErr(w, http.StatusInternalServerError, "結算 taker proceeds 失敗: "+err.Error())
					return
				}
			}
			// Fee 入 fee account。
			if feeAmt > 0 {
				_ = s.node.walletSvc.InternalTransfer(orderbook.EscrowAccount, orderbook.FeeAccount, quoteAsset,
					wallet.NewAmountF(feeAmt, quoteAsset), "ob:sell-fee")
			}
		}
	}

	// 4) 買單 taker：退還價格改善剩餘 quote（若成交價低於掛單價）。
	if req.Side == orderbook.SideBuy {
		lockedQuote := req.Price * req.Amount * (1 + takerRate)
		usedQuote := 0.0
		for _, f := range res.Fills {
			// 每筆成交使用的 quote = price*amount + fee。
			usedQuote += f.Price * f.Amount * (1 + takerRate)
		}
		leftover := lockedQuote - usedQuote
		if leftover > 1e-9 {
			_ = s.node.walletSvc.InternalTransfer(orderbook.EscrowAccount, maker, quoteAsset,
				wallet.NewAmountF(leftover, quoteAsset), "ob:price-improvement-refund")
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"order":     res.Order,
		"fills":     res.Fills,
		"remaining": res.Remaining,
		"market":    normMarket,
		"base":      string(baseAsset),
		"quote":     string(quoteAsset),
	})
}

// handleOrderBookCancel POST /api/orderbook/cancel
func (s *RPCServer) handleOrderBookCancel(w http.ResponseWriter, r *http.Request) {
	if s.node.obSt == nil || s.node.walletSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "OrderBook 未啟用")
		return
	}
	var req struct {
		Address string `json:"address"`
		OrderID int64  `json:"order_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	caller, err := s.node.normalizeEthAddr(req.Address)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "address 無效")
		return
	}
	cr, err := s.node.obSt.CancelOrder(req.OrderID, caller)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// 退還鎖定金額。
	baseAsset, quoteAsset, _ := obAssets(cr.Order.Market)
	_, takerRate := s.node.obSt.FeeRates()
	if cr.Order.Side == orderbook.SideBuy {
		// 買單：退還 quote = price * remaining * (1+takerRate)。
		refundQuote := cr.Order.Price * cr.Remaining * (1 + takerRate)
		if refundQuote > 1e-9 {
			_ = s.node.walletSvc.InternalTransfer(orderbook.EscrowAccount, caller, quoteAsset,
				wallet.NewAmountF(refundQuote, quoteAsset), "ob:cancel-refund-quote")
		}
	} else {
		// 賣單：退還 base = remaining。
		if cr.Remaining > 1e-9 {
			_ = s.node.walletSvc.InternalTransfer(orderbook.EscrowAccount, caller, baseAsset,
				wallet.NewAmountF(cr.Remaining, baseAsset), "ob:cancel-refund-base")
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"order":     cr.Order,
		"remaining": cr.Remaining,
	})
}

// handleOrderBookDepth GET /api/orderbook/depth?market=
func (s *RPCServer) handleOrderBookDepth(w http.ResponseWriter, r *http.Request) {
	if s.node.obSt == nil {
		writeErr(w, http.StatusServiceUnavailable, "OrderBook 未啟用")
		return
	}
	market := r.URL.Query().Get("market")
	d, err := s.node.obSt.Depth(market, 20)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "depth": d})
}

// handleOrderBookTrades GET /api/orderbook/trades?market=
func (s *RPCServer) handleOrderBookTrades(w http.ResponseWriter, r *http.Request) {
	if s.node.obSt == nil {
		writeErr(w, http.StatusServiceUnavailable, "OrderBook 未啟用")
		return
	}
	market := r.URL.Query().Get("market")
	tr, err := s.node.obSt.RecentTrades(market, 50)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "trades": tr})
}

// handleOrderBookMarkets GET /api/orderbook/markets
func (s *RPCServer) handleOrderBookMarkets(w http.ResponseWriter, r *http.Request) {
	if s.node.obSt == nil {
		writeErr(w, http.StatusServiceUnavailable, "OrderBook 未啟用")
		return
	}
	ms, err := s.node.obSt.Markets()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "markets": ms})
}

// handleOrderBookMyOrders GET /api/orderbook/myorders?address=&market=
func (s *RPCServer) handleOrderBookMyOrders(w http.ResponseWriter, r *http.Request) {
	if s.node.obSt == nil {
		writeErr(w, http.StatusServiceUnavailable, "OrderBook 未啟用")
		return
	}
	addr := r.URL.Query().Get("address")
	caller, err := s.node.normalizeEthAddr(addr)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "address 無效")
		return
	}
	market := r.URL.Query().Get("market")
	ords, err := s.node.obSt.MyOrders(caller, market, 100)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "orders": ords})
}

var _ = strconv.Itoa // 保留（日後擴充）
