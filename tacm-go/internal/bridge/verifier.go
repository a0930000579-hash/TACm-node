package bridge

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"sync"
	"time"

	"tacm/internal/crypto"
)

// VerifyResult 為跨鏈消息驗證結果。
type VerifyResult struct {
	OK                 bool   `json:"ok"`
	MessageID          string `json:"message_id,omitempty"`
	SignaturesVerified int    `json:"signatures_verified"`
	Error              string `json:"error,omitempty"`
}

// MessageVerifier 以真實 ECDSA 多重簽名守衛跨鏈消息，並做時效與重放防護。
type MessageVerifier struct {
	required  int
	mu        sync.Mutex
	processed map[string]bool
}

// NewMessageVerifier 構造守衛驗證器。
func NewMessageVerifier(required int) *MessageVerifier {
	if required <= 0 {
		required = 2
	}
	return &MessageVerifier{
		required:  required,
		processed: map[string]bool{},
	}
}

// Required 返回簽名門檻。
func (v *MessageVerifier) Required() int { return v.required }

func failVerify(msg string) *VerifyResult {
	return &VerifyResult{OK: false, Error: msg}
}

// Verify 驗證跨鏈消息：格式、鏈、時效、重放，最後統計有效 ECDSA 簽名是否達門檻。
func (v *MessageVerifier) Verify(m *CrossChainMessage,
	validators []Validator) (*VerifyResult, error) {
	if m == nil {
		return failVerify("nil message"), nil
	}
	if m.MessageID == "" || m.SourceChain == "" || m.TargetChain == "" {
		return failVerify("message incomplete"), nil
	}
	chains := SupportedChains()
	if _, ok := chains[m.SourceChain]; !ok {
		return failVerify("unsupported source chain: " + m.SourceChain), nil
	}
	if _, ok := chains[m.TargetChain]; !ok {
		return failVerify("unsupported target chain: " + m.TargetChain), nil
	}
	if time.Now().Unix()-m.Ts > 86400 {
		return failVerify("message expired (>24h)"), nil
	}

	nonceKey := m.SourceChain + ":" + strconv.FormatInt(m.Nonce, 10)
	v.mu.Lock()
	if v.processed[nonceKey] {
		v.mu.Unlock()
		return failVerify("nonce already used (replay rejected)"), nil
	}
	v.mu.Unlock()

	pubByAddr := map[string]string{}
	known := map[string]bool{}
	for _, val := range validators {
		if val.Active {
			pubByAddr[val.Address] = val.PublicKey
			known[val.Address] = true
		}
	}

	digest := m.SigningDigest()
	counted := map[string]bool{}
	valid := 0
	for _, s := range m.Signatures {
		addr := s.Validator
		if counted[addr] || !known[addr] {
			continue
		}
		pubBytes, err := hex.DecodeString(pubByAddr[addr])
		if err != nil {
			continue
		}
		sigBytes, err := hex.DecodeString(s.Signature)
		if err != nil {
			continue
		}
		if crypto.VerifyDigest(pubBytes, digest, sigBytes) {
			counted[addr] = true
			valid++
		}
	}

	if valid < v.required {
		return &VerifyResult{
			OK:                 false,
			SignaturesVerified: valid,
			Error: fmt.Sprintf("insufficient signatures: need %d, have %d",
				v.required, valid),
		}, nil
	}

	v.mu.Lock()
	v.processed[nonceKey] = true
	v.mu.Unlock()
	return &VerifyResult{
		OK: true, MessageID: m.MessageID,
		SignaturesVerified: valid,
	}, nil
}
