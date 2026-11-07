package bft

import (
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"tacm/internal/crypto"
)

// CanonicalVote 返回投票的規範字節，為簽名與驗證的唯一來源。
func CanonicalVote(voteType string, height int64, round int32, blockHash *string) []byte {
	bh := "nil"
	if blockHash != nil {
		bh = *blockHash
	}
	s := fmt.Sprintf("tacm-bft/%s/%d/%d/%s", voteType, height, round, bh)
	return []byte(s)
}

// SigningBytes 返回本投票待簽名的規範字節。
func (v *Vote) SigningBytes() []byte {
	return CanonicalVote(v.Type, v.Height, v.Round, v.BlockHash)
}

// SignVote 用密鑰對簽署一張投票並返回完整 Vote。
func SignVote(kp *crypto.KeyPair, voteType string, height int64, round int32, blockHash *string) (*Vote, error) {
	if voteType != Prevote && voteType != Precommit {
		return nil, fmt.Errorf("bft: 非法投票類型: %s", voteType)
	}
	address, err := kp.Address()
	if err != nil {
		return nil, err
	}
	sig, err := kp.Sign(CanonicalVote(voteType, height, round, blockHash))
	if err != nil {
		return nil, fmt.Errorf("bft: 簽署投票失敗: %w", err)
	}
	return &Vote{
		Type:      voteType,
		Height:    height,
		Round:     round,
		BlockHash: blockHash,
		Validator: address,
		Signature: hex.EncodeToString(sig),
		Ts:        time.Now().Unix(),
	}, nil
}

// VerifyVote 驗證一張投票：簽名者在驗證人集、簽名有效且與內容匹配。
func VerifyVote(v *Vote, vs *ValidatorSet) error {
	val := vs.Get(v.Validator)
	if val == nil {
		return errors.New("簽名者不在驗證人集")
	}
	if v.Type != Prevote && v.Type != Precommit {
		return fmt.Errorf("非法投票類型: %s", v.Type)
	}
	if v.Signature == "" {
		return errors.New("缺少簽名")
	}
	pub, err := hex.DecodeString(val.PubkeyHex)
	if err != nil {
		return fmt.Errorf("公鑰非法: %w", err)
	}
	sig, err := hex.DecodeString(v.Signature)
	if err != nil {
		return fmt.Errorf("簽名非法: %w", err)
	}
	if !crypto.VerifyWithPublicKey(pub, v.SigningBytes(), sig) {
		return errors.New("簽名驗證失敗（可能被篡改）")
	}
	return nil
}

// bucketKey 把可空塊哈希轉為 map 鍵（nil → ""）。
func bucketKey(blockHash *string) string {
	if blockHash == nil {
		return ""
	}
	return *blockHash
}

func ptrString(s string) *string { return &s }
