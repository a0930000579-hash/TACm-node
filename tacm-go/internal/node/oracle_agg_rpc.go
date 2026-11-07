package node

import (
	"net/http"
)

// ---------- v156：預言機去中心化多源聚合 RPC ----------
// 兩個唯讀端點，讀 s.node.oracleSvc（*oracle.Oracle）的多源聚合視圖：
//   - GET /api/oracle/sources      各數據源狀態/權重/異常
//   - GET /api/oracle/aggregate?sym= 聚合價/中位數/置信度/採用源數/degraded

// handleOracleSources GET /api/oracle/sources — 多數據源狀態清單。
func (s *RPCServer) handleOracleSources(w http.ResponseWriter, r *http.Request) {
	if s.node.oracleSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "oracle not initialized")
		return
	}
	srcs := s.node.oracleSvc.SourcesStatus()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"sources": srcs,
	})
}

// handleOracleAggregateDetail GET /api/oracle/aggregate?sym=BTC/USDT — 單一 symbol 聚合詳情。
func (s *RPCServer) handleOracleAggregateDetail(w http.ResponseWriter, r *http.Request) {
	if s.node.oracleSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "oracle not initialized")
		return
	}
	sym := r.URL.Query().Get("sym")
	if sym == "" {
		sym = r.URL.Query().Get("symbol") // 相容別名
	}
	if sym == "" {
		writeErr(w, http.StatusBadRequest, "sym required")
		return
	}
	res, ok := s.node.oracleSvc.AggregateDetail(sym)
	if !ok {
		writeErr(w, http.StatusNotFound, "no aggregate data for: "+sym)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "aggregate": res})
}
