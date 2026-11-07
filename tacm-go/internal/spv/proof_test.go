package spv

import (
	"encoding/hex"
	"strconv"
	"testing"

	"tacm/internal/crypto"
)

func TestMerkleProofRoundTrip(t *testing.T) {
	hashes := []string{}
	for i := 0; i < 7; i++ {
		raw := crypto.DoubleSHA256([]byte("tx" + strconv.Itoa(i)))
		hashes = append(hashes, hex.EncodeToString(raw))
	}
	root, _ := crypto.MerkleRootStrings(hashes)

	items := make([]any, len(hashes))
	for i, h := range hashes {
		items[i] = h
	}

	for idx := 0; idx < len(hashes); idx++ {
		proof, _ := crypto.MerkleProof(items, idx)
		if !VerifyMerkleProof(hashes[idx], idx, proof, root) {
			t.Errorf("合法 proof 應通過 idx=%d", idx)
		}
		// 錯誤根應失敗。
		if VerifyMerkleProof(hashes[idx], idx, proof,
			"0000000000000000000000000000000000000000000000000000000000000000") {
			t.Errorf("錯誤根不應通過 idx=%d", idx)
		}
		// 錯誤 index 應失敗（最後葉為奇數複製位，與虛擬位數學等價，排除）。
		if idx < len(hashes)-1 &&
			VerifyMerkleProof(hashes[idx], idx^1, proof, root) {
			t.Errorf("錯誤 index 不應通過 idx=%d", idx)
		}
	}

	// 截斷 proof 應失敗。
	proof, _ := crypto.MerkleProof(items, 2)
	if len(proof) > 0 && VerifyMerkleProof(hashes[2], 2, proof[:len(proof)-1], root) {
		t.Error("截斷 proof 不應通過")
	}
	// 篡改兄弟節點應失敗。
	if len(proof) > 0 {
		bad := append([]string{}, proof...)
		bad[0] = "0000000000000000000000000000000000000000000000000000000000000000"
		if VerifyMerkleProof(hashes[2], 2, bad, root) {
			t.Error("篡改兄弟不應通過")
		}
	}
}
