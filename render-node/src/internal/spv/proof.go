// Package spv 實現 TAC Ai 智能鏈的輕客戶端（SPV）：只同步區塊頭，
// 用 Merkle 包含證明在本地驗證交易，並可結合 BFT 最終性證明防止回滾。
package spv

import (
	"encoding/hex"

	"tacm/internal/crypto"
)

// VerifyMerkleProof 從交易哈希（葉子）按兄弟節點本地重算到根並比對。
// 規則與 crypto.MerkleRoot/MerkleProof 完全一致：
// 葉子=SHA256(tx_hash_hex)，每層=SHA256(left||right)。
func VerifyMerkleProof(txHashHex string, index int,
	siblings []string, rootHex string) bool {
	h := crypto.SHA256([]byte(txHashHex))
	idx := index
	for _, sibHex := range siblings {
		sib, err := hex.DecodeString(sibHex)
		if err != nil {
			return false
		}
		if idx&1 == 1 {
			// 目標在右：SHA256(sibling || h)
			h = crypto.SHA256(append(append([]byte{}, sib...), h...))
		} else {
			// 目標在左：SHA256(h || sibling)
			h = crypto.SHA256(append(append([]byte{}, h...), sib...))
		}
		idx >>= 1
	}
	return hex.EncodeToString(h) == rootHex
}
