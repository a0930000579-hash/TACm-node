package node

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"tacm/internal/rollup"
)

func (s *RPCServer) requireL2(w http.ResponseWriter) *rollup.Rollup {
	if s.node.l2 == nil {
		writeErr(w, http.StatusServiceUnavailable,
			"Layer2 not enabled (start node with -l2)")
		return nil
	}
	return s.node.l2
}

func decodeBody(r *http.Request) (map[string]any, error) {
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	var body map[string]any
	if err := dec.Decode(&body); err != nil {
		return nil, err
	}
	return body, nil
}

func bodyInt64(body map[string]any, key string) (int64, error) {
	switch v := body[key].(type) {
	case json.Number:
		return v.Int64()
	case float64:
		return int64(v), nil
	}
	return 0, fmt.Errorf("missing/invalid %s", key)
}

func bodyFloat(body map[string]any, key string) (float64, error) {
	switch v := body[key].(type) {
	case json.Number:
		return v.Float64()
	case float64:
		return v, nil
	}
	return 0, fmt.Errorf("missing/invalid %s", key)
}

func (s *RPCServer) handleL2Status(w http.ResponseWriter, r *http.Request) {
	l2 := s.requireL2(w)
	if l2 == nil {
		return
	}
	st, err := l2.Status()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *RPCServer) handleL2Account(w http.ResponseWriter, r *http.Request) {
	l2 := s.requireL2(w)
	if l2 == nil {
		return
	}
	writeJSON(w, http.StatusOK, l2.Account(r.PathValue("address")))
}

func (s *RPCServer) handleL2Block(w http.ResponseWriter, r *http.Request) {
	l2 := s.requireL2(w)
	if l2 == nil {
		return
	}
	h, err := strconv.ParseInt(r.PathValue("height"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad height")
		return
	}
	b := l2.Block(h)
	if b == nil {
		writeErr(w, http.StatusNotFound, "block not found")
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *RPCServer) handleL2Tx(w http.ResponseWriter, r *http.Request) {
	l2 := s.requireL2(w)
	if l2 == nil {
		return
	}
	tx, err := l2.Tx(r.PathValue("hash"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if tx == nil {
		writeErr(w, http.StatusNotFound, "tx not found")
		return
	}
	writeJSON(w, http.StatusOK, tx)
}

func (s *RPCServer) handleL2TxSubmit(w http.ResponseWriter, r *http.Request) {
	l2 := s.requireL2(w)
	if l2 == nil {
		return
	}
	tx, err := decodeBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	h, err := l2.SubmitTransaction(tx)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"tx_hash": h, "status": "pending"})
}

func (s *RPCServer) handleL2Batch(w http.ResponseWriter, r *http.Request) {
	l2 := s.requireL2(w)
	if l2 == nil {
		return
	}
	b, err := l2.CreateBatch()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if b == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "empty mempool"})
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *RPCServer) handleL2Submit(w http.ResponseWriter, r *http.Request) {
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
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := l2.SubmitToL1(h)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *RPCServer) handleL2Finalize(w http.ResponseWriter, r *http.Request) {
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
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := l2.FinalizeBlock(h, time.Now().Unix()); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "height": h, "status": "finalized"})
}

func (s *RPCServer) handleL2Challenge(w http.ResponseWriter, r *http.Request) {
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
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := l2.ChallengeBlock(h, time.Now().Unix())
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *RPCServer) handleL2Deposit(w http.ResponseWriter, r *http.Request) {
	l2 := s.requireL2(w)
	if l2 == nil {
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	address, _ := body["address"].(string)
	amount, err := bodyFloat(body, "amount")
	if err != nil || address == "" {
		writeErr(w, http.StatusBadRequest, "address and amount required")
		return
	}
	l1Hash, _ := body["l1_tx_hash"].(string)
	if l1Hash == "" {
		l1Hash = fmt.Sprintf("manual_%d", time.Now().UnixNano())
	}
	res, err := l2.DepositToL2(address, amount, l1Hash)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *RPCServer) handleL2Withdraw(w http.ResponseWriter, r *http.Request) {
	l2 := s.requireL2(w)
	if l2 == nil {
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	address, _ := body["address"].(string)
	amount, err := bodyFloat(body, "amount")
	if err != nil || address == "" {
		writeErr(w, http.StatusBadRequest, "address and amount required")
		return
	}
	res, err := l2.WithdrawToL1(address, amount)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}
