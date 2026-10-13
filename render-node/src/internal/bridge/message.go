package bridge

import (
	"encoding/hex"
	"fmt"
	"time"

	"tacm/internal/crypto"
)

// 跨鏈消息類型。
const (
	MsgLock   = "lock"
	MsgBurn   = "burn"
	MsgMint   = "mint"
	MsgUnlock = "unlock"
	MsgUpdate = "update"
)

// CrossChainMessage 為跨鏈間傳遞的指令消息。
type CrossChainMessage struct {
	MessageID   string         `json:"message_id"`
	SourceChain string         `json:"source_chain"`
	TargetChain string         `json:"target_chain"`
	Type        string         `json:"message_type"`
	Payload     map[string]any `json:"payload"`
	Nonce       int64          `json:"nonce"`
	Ts          int64          `json:"timestamp"`
	Signatures  []ValidatorSig `json:"signatures"`
}

// NewMessage 構造跨鏈消息並計算確定性 message_id。
func NewMessage(sourceChain, targetChain, mtype string,
	payload map[string]any, nonce int64) (*CrossChainMessage, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	m := &CrossChainMessage{
		SourceChain: sourceChain, TargetChain: targetChain,
		Type: mtype, Payload: payload, Nonce: nonce,
		Ts: time.Now().Unix(),
	}
	id, err := m.computeID()
	if err != nil {
		return nil, err
	}
	m.MessageID = id
	return m, nil
}

func (m *CrossChainMessage) computeID() (string, error) {
	doc := map[string]any{
		"source_chain": m.SourceChain,
		"target_chain": m.TargetChain,
		"message_type": m.Type,
		"payload":      m.Payload,
		"nonce":        m.Nonce,
		"timestamp":    m.Ts,
	}
	raw, err := crypto.Canonical(doc)
	if err != nil {
		return "", fmt.Errorf("bridge: 序列化消息失敗: %w", err)
	}
	return hex.EncodeToString(crypto.SHA256(raw)), nil
}

// SigningDigest 返回全體驗證人共同簽名的 32 字節摘要 = SHA256(message_id)。
func (m *CrossChainMessage) SigningDigest() []byte {
	return crypto.SHA256([]byte(m.MessageID))
}

// Sign 以密鑰對對消息簽名（直接簽摘要，DER→hex）。
func (m *CrossChainMessage) Sign(kp *crypto.KeyPair) (ValidatorSig, error) {
	addr, err := kp.Address()
	if err != nil {
		return ValidatorSig{}, err
	}
	sig, err := kp.SignDigestKP(m.SigningDigest())
	if err != nil {
		return ValidatorSig{}, err
	}
	return ValidatorSig{
		Validator: addr,
		Signature: hex.EncodeToString(sig),
		Ts:        time.Now().Unix(),
	}, nil
}

// AddSignature 收集一個驗證人簽名。
func (m *CrossChainMessage) AddSignature(s ValidatorSig) {
	m.Signatures = append(m.Signatures, s)
}
