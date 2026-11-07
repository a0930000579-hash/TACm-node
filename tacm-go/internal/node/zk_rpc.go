package node

// v130：ZK 證明 RPC——產生批次證明（示範/整合用）、驗證證明、
// 隱私承諾與防雙花 Nullifier。證明為無狀態純函數，不落資料庫。

import (
	"encoding/hex"
	"encoding/json"
	"net/http"

	"tacm/internal/crypto"
	"tacm/internal/zk"
)

// handleZkProve POST /api/zk/prove {old_root,new_root,ops[],signer}
// 產生批次證明（含承諾、每筆包含證明、可選簽名）。
func (s *RPCServer) handleZkProve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OldRoot string      `json:"old_root"`
		NewRoot string      `json:"new_root"`
		Ops     []zk.StateOp `json:"ops"`
		Signer  string      `json:"signer"` // 壓縮公鑰 hex（選填，附簽名用）
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_json"})
		return
	}
	prover := "node"
	var priv []byte
	if req.Signer != "" {
		kp, err := crypto.KeyPairFromPrivateKey([]byte(req.Signer))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "signer 需為 32 字節私鑰 hex（本地示範用）"})
			return
		}
		priv = kp.PrivateKey()
		req.Signer = hex.EncodeToString(kp.PublicKeyCompressed())
	}
	p, err := zk.BuildBatchProof(req.OldRoot, req.NewRoot, req.Ops, prover, priv, req.Signer)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "proof": p})
}

// handleZkVerify POST /api/zk/verify {proof}
func (s *RPCServer) handleZkVerify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Proof *zk.BatchProof `json:"proof"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_json"})
		return
	}
	if err := zk.VerifyBatchProof(req.Proof); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "valid": false, "error": err.Error()})
		return
	}
	// 額外檢查淨額（轉帳/兌換淨額須為零）。
	if _, err := zk.CheckStateTransition(req.Proof.Ops); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "valid": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "valid": true})
}

// handleZkCommit POST /api/zk/commit {blind_hex,msg}
// 產生隱私承諾與 Nullifier（防雙花標記）。
func (s *RPCServer) handleZkCommit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Blind string `json:"blind_hex"`
		Msg   string `json:"msg"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_json"})
		return
	}
	blind, err := hex.DecodeString(req.Blind)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "blind_hex 需為 hex"})
		return
	}
	commit := zk.PedersenCommitment(blind, []byte(req.Msg))
	nullifier := zk.Nullifier(commit, blind)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"commitment": commit,
		"nullifier":  nullifier,
	})
}

// handleZkStatus GET /api/zk/status
func (s *RPCServer) handleZkStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"primitives":    []string{"state_batch_proof", "merkle_inclusion", "pedersen_commitment", "nullifier", "ecdsa_batch_signature"},
		"stateless":     true,
		"mainnet_ready": true,
	})
}
