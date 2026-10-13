package node

// M140 合約 ABI/Source 驗證中心：
//  - POST /contract/verify   上傳合約名稱/原始碼/ABI，校驗合約存在後持久化
//  - GET  /contract/abi/{address}   回已驗證合約的 ABI（供前端自動化 read）
//  - GET  /contract/verified  已驗證合約清單
// 元資料存於 data-dir contracts_meta.json（atomic write，重啟保留）。

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"tacm/internal/crypto"
)

// ContractMeta 為已驗證合約的 ABI/Source 記錄。
type ContractMeta struct {
	Address    string `json:"address"`   // tx0 格式（對外）
	EvmAddress string `json:"evm_address"` // 內部 0x 格式
	Name       string `json:"name"`
	Source     string `json:"source,omitempty"`
	ABI        string `json:"abi"`
	VerifiedAt int64  `json:"verified_at"`
	TxHash     string `json:"tx_hash,omitempty"`
}

// loadContractMeta 從 data-dir 載入已驗證合約清單（啟動時呼叫）。
func (n *Node) loadContractMeta() {
	n.metaMu.Lock()
	defer n.metaMu.Unlock()
	raw, err := os.ReadFile(n.metaPath)
	if err != nil {
		return // 首次啟動無檔，忽略
	}
	var list []ContractMeta
	if json.Unmarshal(raw, &list) != nil {
		return // 檔損壞時不阻塞啟動，下次寫入覆蓋
	}
	for _, m := range list {
		if m.EvmAddress != "" && crypto.IsValidAddress(m.EvmAddress) {
			n.verifyMeta[m.EvmAddress] = m
		}
	}
}

// saveContractMeta 原子寫入 data-dir（每次 verify 成功後呼叫）。
func (n *Node) saveContractMeta() error {
	n.metaMu.Lock()
	defer n.metaMu.Unlock()
	list := make([]ContractMeta, 0, len(n.verifyMeta))
	for _, m := range n.verifyMeta {
		list = append(list, m)
	}
	raw, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := n.metaPath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, n.metaPath)
}

// handleContractVerify POST /contract/verify
func (s *RPCServer) handleContractVerify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Address string `json:"address"`
		Name    string `json:"name"`
		Source  string `json:"source"`
		ABI     string `json:"abi"`
		TxHash  string `json:"tx_hash,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	if req.Address == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "address required"})
		return
	}
	evm, err := normalizeAddrTo0x(req.Address)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	if len(s.node.contracts.GetCode(evm)) == 0 {
		writeJSON(w, http.StatusNotFound,
			map[string]string{"error": "contract not found or empty code — 請先部署合約（Token Studio / POST /contract/deploy）再驗證"})
		return
	}
	if req.ABI == "" || !json.Valid([]byte(req.ABI)) {
		writeJSON(w, http.StatusBadRequest,
			map[string]string{"error": "ABI 需為合法 JSON 字串（陣列，如 [{\"name\":\"totalSupply\",\"inputs\":[],\"outputs\":[{\"type\":\"uint256\"}]}]）"})
		return
	}
	// 基本 ABI 內容校驗：至少為 JSON 陣列。
	if !strings.HasPrefix(strings.TrimSpace(req.ABI), "[") {
		writeJSON(w, http.StatusBadRequest,
			map[string]string{"error": "ABI 需為 JSON 陣列"})
		return
	}
	tx0, _ := evmToTx0(evm)
	meta := ContractMeta{
		Address:    tx0,
		EvmAddress: evm,
		Name:       req.Name,
		Source:     req.Source,
		ABI:        req.ABI,
		VerifiedAt: time.Now().Unix(),
		TxHash:     req.TxHash,
	}
	s.node.metaMu.Lock()
	s.node.verifyMeta[evm] = meta
	s.node.metaMu.Unlock()
	if serr := s.node.saveContractMeta(); serr != nil {
		writeJSON(w, http.StatusInternalServerError, errBody(serr))
		return
	}
	// 統計 ABI 內方法數（供前端展示）。
	methodCount := countABIMethods(req.ABI)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "verified": true, "address": tx0,
		"name": req.Name, "methods": methodCount,
	})
}

// countABIMethods 統計 ABI 中 type=function 的條目數。
func countABIMethods(abi string) int {
	var entries []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal([]byte(abi), &entries) != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.Type == "function" || e.Type == "" {
			n++
		}
	}
	return n
}

// handleContractABI GET /contract/abi/{address}
func (s *RPCServer) handleContractABI(w http.ResponseWriter, r *http.Request) {
	addr := r.PathValue("address")
	evm, err := normalizeAddrTo0x(addr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	s.node.metaMu.Lock()
	meta, ok := s.node.verifyMeta[evm]
	s.node.metaMu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound,
			map[string]string{"error": "contract not verified — 請先在合約驗證中心上傳 ABI（POST /contract/verify）"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"address":     meta.Address,
		"evm_address": meta.EvmAddress,
		"name":        meta.Name,
		"source":      meta.Source,
		"abi":         meta.ABI,
		"verified_at": meta.VerifiedAt,
	})
}

// handleContractVerified GET /contract/verified
func (s *RPCServer) handleContractVerified(w http.ResponseWriter, r *http.Request) {
	s.node.metaMu.Lock()
	defer s.node.metaMu.Unlock()
	out := make([]ContractMeta, 0, len(s.node.verifyMeta))
	for _, m := range s.node.verifyMeta {
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"verified": out, "count": len(out),
	})
}
