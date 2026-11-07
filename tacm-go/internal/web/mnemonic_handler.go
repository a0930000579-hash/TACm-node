package web

import (
	"encoding/json"
	"net/http"

	"tacm/internal/crypto"
	"tacm/internal/wallet"
)

// handleMnemonicGenerate POST /api/wallet/mnemonic/generate — 由 WIF 生成 24 詞助記詞（備份）。
func (s *Server) handleMnemonicGenerate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WIF string `json:"wif"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]any{"error": "JSON 格式錯誤"})
		return
	}
	if _, err := crypto.WIFToPrivateKey(req.WIF); err != nil {
		writeJSON(w, map[string]any{"error": "WIF 無效"})
		return
	}
	m, err := wallet.WIFToMnemonic(req.WIF)
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"mnemonic": m})
}

// handleMnemonicRestore POST /api/wallet/mnemonic/restore — 助記詞還原 WIF + 地址（導入）。
func (s *Server) handleMnemonicRestore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mnemonic string `json:"mnemonic"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]any{"error": "JSON 格式錯誤"})
		return
	}
	wif, err := wallet.MnemonicToWIF(req.Mnemonic)
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	kp, err := crypto.KeyPairFromWIF(wif)
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	addr, err := kp.Address()
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"wif": wif, "address": addr})
}
