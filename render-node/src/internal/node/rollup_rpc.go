package node

// v156：Rollup 有效性證明 RPC（A3）。
// 全部 handler 掛在 *RPCServer，複用既有 s.node.l2（*rollup.Rollup），
// 不新增重複服務欄位。路由由整合者在 rpc.go 統一 addRouteDoc 註冊。

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"tacm/internal/zk"
)

// handleRollupBatchSubmit POST /api/rollup/batch/submit
// 收集內存池成批次、打包、（若配置 L1）提交承諾根，回傳批次＋有效性證明。
func (s *RPCServer) handleRollupBatchSubmit(w http.ResponseWriter, r *http.Request) {
	l2 := s.requireL2(w)
	if l2 == nil {
		return
	}
	block, err := l2.CreateBatch()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if block == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "error": "mempool empty", "status": "empty",
		})
		return
	}
	// 嘗試提交 L1（未配置 L1 時保留 pending，不影響回傳）。
	submitted := false
	l1Tx := ""
	if sub, err := l2.SubmitToL1(block.Height); err == nil {
		submitted = true
		l1Tx = sub.L1TxHash
	}
	view, _ := l2.BatchWithProof(block.Height)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"batch":        view,
		"l1_submitted": submitted,
		"l1_tx_hash":   l1Tx,
	})
}

// handleRollupBatch GET /api/rollup/batch?index=
// 帶 index 回單一批次＋有效性證明；不帶 index 回批次列表（最近 50）。
func (s *RPCServer) handleRollupBatch(w http.ResponseWriter, r *http.Request) {
	l2 := s.requireL2(w)
	if l2 == nil {
		return
	}
	if idx := r.URL.Query().Get("index"); idx != "" {
		h, err := strconv.ParseInt(idx, 10, 64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad index")
			return
		}
		view, _ := l2.BatchWithProof(h)
		if view == nil {
			writeErr(w, http.StatusNotFound, "batch not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "batch": view})
		return
	}
	list := l2.BatchesList(50)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "batches": list})
}

// handleRollupProofVerify POST /api/rollup/proof/verify
// body: {"index": N} 驗證本機已存批次；或 {"proof": {...}} 無狀態驗證外來證明。
func (s *RPCServer) handleRollupProofVerify(w http.ResponseWriter, r *http.Request) {
	l2 := s.requireL2(w)
	if l2 == nil {
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	// 模式一：驗證本機批次。
	if idxV, ok := body["index"]; ok {
		h, err := bodyInt64(map[string]any{"index": idxV}, "index")
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad index")
			return
		}
		if err := l2.VerifyBatchValidity(h); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"ok": true, "valid": false, "height": h, "error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "valid": true, "height": h, "verified_at": time.Now().Unix(),
		})
		return
	}
	// 模式二：無狀態驗證外來證明（由前端直接貼上）。
	raw, err := json.Marshal(body["proof"])
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad proof json: "+err.Error())
		return
	}
	var p zk.RollupValidityProof
	if err := json.Unmarshal(raw, &p); err != nil {
		writeErr(w, http.StatusBadRequest, "bad proof: "+err.Error())
		return
	}
	if err := zk.VerifyRollupValidityProof(&p); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "valid": false, "error": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "valid": true, "verified_at": time.Now().Unix(),
	})
}

// handleRollupStateRoot GET /api/rollup/state-root
// 回當前狀態根、高度、賬戶數、最近批次有效性旗標。
func (s *RPCServer) handleRollupStateRoot(w http.ResponseWriter, r *http.Request) {
	l2 := s.requireL2(w)
	if l2 == nil {
		return
	}
	view, err := l2.CurrentStateRoot()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleRollupChallenge POST /api/rollup/challenge
// body: {"height": N} 在挑戰期內對該批次提欺詐挑戰（基於 pre-snapshot 重放）。
func (s *RPCServer) handleRollupChallenge(w http.ResponseWriter, r *http.Request) {
	l2 := s.requireL2(w)
	if l2 == nil {
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	h, err := bodyInt64(body, "height")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "height required")
		return
	}
	res, err := l2.ChallengeBlock(h, time.Now().Unix())
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}
