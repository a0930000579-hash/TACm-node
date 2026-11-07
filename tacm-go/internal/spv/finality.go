package spv

import (
	"tacm/internal/consensus/bft"
)

func voteFromMap(m map[string]any) bft.Vote {
	var blockHash *string
	if bh, ok := m["block_hash"].(string); ok && bh != "" {
		bhCopy := bh
		blockHash = &bhCopy
	}
	return bft.Vote{
		Type:      hdrStr(m, "vote_type"),
		Height:    hdrInt(m, "height"),
		Round:     int32(hdrInt(m, "round")),
		BlockHash: blockHash,
		Validator: hdrStr(m, "validator"),
		Signature: hdrStr(m, "signature"),
		Ts:        hdrInt(m, "ts"),
	}
}

// CheckFinalityProof 統計對指定（高度,塊哈希）的有效 precommit 權重，
// 同一驗證人只計一次，需達 BFT quorum（>2/3）。
func CheckFinalityProof(height int64, blockHash string,
	voteMaps []map[string]any, vs *bft.ValidatorSet) (bool, int) {
	power := 0
	seen := map[string]bool{}
	for _, vm := range voteMaps {
		vote := voteFromMap(vm)
		if seen[vote.Validator] {
			continue
		}
		if vote.Type != bft.Precommit || vote.Height != height ||
			vote.BlockHash == nil || *vote.BlockHash != blockHash {
			continue
		}
		if err := bft.VerifyVote(&vote, vs); err != nil {
			continue
		}
		if val := vs.Get(vote.Validator); val != nil {
			power += val.Power
			seen[vote.Validator] = true
		}
	}
	return vs.HasQuorum(power), power
}
