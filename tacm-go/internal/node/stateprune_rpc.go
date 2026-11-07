package node

import (
	"net/http"

	"tacm/internal/stateprune"
)

// handleStatePruneStats GET /api/stateprune/stats — 唯讀回報狀態膨脹治理（歷史清理）統計。
// 節點未啟用 pruner（n.pruner == nil）時回合理空值，勿 panic。
func (s *RPCServer) handleStatePruneStats(w http.ResponseWriter, r *http.Request) {
	if s.node == nil || s.node.pruner == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"enabled": false,
			"stats":   stateprune.Stats{},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"enabled": true,
		"stats":   s.node.pruner.Stats(),
	})
}
