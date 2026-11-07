package node

// v131：可驗證隨機函數（ECVRF）RPC——產生密鑰、產生證明、驗證證明。
// 鏈下可驗證隨機數：公平抽籤/節點選舉/NFT 白名單等。

import (
	"encoding/json"
	"net/http"

	"tacm/internal/vrf"
)

// handleVrfKeygen POST /api/vrf/keygen
func (s *RPCServer) handleVrfKeygen(w http.ResponseWriter, r *http.Request) {
	kp, err := vrf.GenerateKey()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pub": kp.Pub})
}

// handleVrfProve POST /api/vrf/prove {priv_hex, message}
func (s *RPCServer) handleVrfProve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PrivHex string `json:"priv_hex"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_json"})
		return
	}
	kp, err := vrf.KeyFromPrivate(req.PrivHex)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	p, err := kp.Prove([]byte(req.Message))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pub": kp.Pub, "proof": p})
}

// handleVrfVerify POST /api/vrf/verify {pub, proof}
func (s *RPCServer) handleVrfVerify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pub   string     `json:"pub"`
		Proof *vrf.Proof `json:"proof"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_json"})
		return
	}
	ok, err := vrf.Verify(req.Pub, req.Proof)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "valid": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "valid": ok})
}

// handleVrfStatus GET /api/vrf/status
func (s *RPCServer) handleVrfStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"scheme":      "ECVRF-schnorr-secp256k1",
		"output_size": 32,
		"mainnet_ready": true,
	})
}
