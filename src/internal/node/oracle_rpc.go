package node

import (
	"encoding/json"
	"net/http"
)

// ---------- M152 鏈上預言機 RPC ----------

// oraclePriceJSON 喂價輸出。
type oraclePriceJSON struct {
	Symbol    string  `json:"symbol"`
	Base      string  `json:"base"`
	Price     float64 `json:"price"`
	Chg24h    float64 `json:"chg24h"`
	Source    string  `json:"source"`
	Live      bool    `json:"live"`
	UpdatedAt int64   `json:"updated_at"`
}

// handleOraclePrices GET /oracle/prices — 全部喂價快照。
func (s *RPCServer) handleOraclePrices(w http.ResponseWriter, r *http.Request) {
	if s.node.oracleSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "oracle not initialized")
		return
	}
	px := s.node.oracleSvc.Snapshot()
	out := make([]oraclePriceJSON, 0, len(px))
	for _, p := range px {
		out = append(out, oraclePriceJSON{
			Symbol: p.Symbol, Base: p.Base, Price: p.Price, Chg24h: p.Chg24h,
			Source: p.Source, Live: p.Live, UpdatedAt: p.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "prices": out, "uptime": s.node.oracleSvc.Uptime(),
	})
}

// handleOraclePrice GET /oracle/price?symbol=BTC/USDT — 單價查詢。
func (s *RPCServer) handleOraclePrice(w http.ResponseWriter, r *http.Request) {
	sym := r.URL.Query().Get("symbol")
	if sym == "" {
		writeErr(w, http.StatusBadRequest, "symbol required")
		return
	}
	if s.node.oracleSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "oracle not initialized")
		return
	}
	p, ok := s.node.oracleSvc.Get(sym)
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown symbol: "+sym)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "price": oraclePriceJSON{
		Symbol: p.Symbol, Base: p.Base, Price: p.Price, Chg24h: p.Chg24h,
		Source: p.Source, Live: p.Live, UpdatedAt: p.UpdatedAt,
	}})
}

// handleOracleFeed POST /oracle/feed — 手動覆寫喂價（節點操作者）。
func (s *RPCServer) handleOracleFeed(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Symbol string  `json:"symbol"`
		Price  float64 `json:"price"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.node.oracleSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "oracle not initialized")
		return
	}
	if err := s.node.oracleSvc.Feed(req.Symbol, req.Price); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "fed": req.Symbol})
}

// handleOracleHistory GET /oracle/history?symbol=BTC/USDT — 歷史喂價。
func (s *RPCServer) handleOracleHistory(w http.ResponseWriter, r *http.Request) {
	sym := r.URL.Query().Get("symbol")
	if sym == "" {
		writeErr(w, http.StatusBadRequest, "symbol required")
		return
	}
	if s.node.oracleSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "oracle not initialized")
		return
	}
	h := s.node.oracleSvc.History(sym)
	out := make([]oraclePriceJSON, 0, len(h))
	for _, p := range h {
		out = append(out, oraclePriceJSON{
			Symbol: p.Symbol, Base: p.Base, Price: p.Price, Chg24h: p.Chg24h,
			Source: p.Source, Live: p.Live, UpdatedAt: p.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "history": out})
}

// handleOracleFeeder POST /oracle/feeder {feeder, symbol, price} — feeder 提交一筆喂價。
func (s *RPCServer) handleOracleFeeder(w http.ResponseWriter, r *http.Request) {
	if s.node.oracleSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "oracle not initialized")
		return
	}
	var req struct {
		Feeder string  `json:"feeder"`
		Symbol string  `json:"symbol"`
		Price  float64 `json:"price"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json: "+err.Error())
		return
	}
	if err := s.node.oracleSvc.SubmitFeeder(req.Feeder, req.Symbol, req.Price); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "submitted": req.Symbol, "feeder": req.Feeder})
}

// handleOracleAggregate POST /oracle/aggregate — 觸發一輪聚合（≥3 feeder 取中位數）。
func (s *RPCServer) handleOracleAggregate(w http.ResponseWriter, r *http.Request) {
	if s.node.oracleSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "oracle not initialized")
		return
	}
	px := s.node.oracleSvc.AggregateRound()
	out := make([]oraclePriceJSON, 0, len(px))
	for _, p := range px {
		out = append(out, oraclePriceJSON{
			Symbol: p.Symbol, Base: p.Base, Price: p.Price, Chg24h: p.Chg24h,
			Source: p.Source, Live: p.Live, UpdatedAt: p.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "prices": out})
}
