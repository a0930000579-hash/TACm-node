package crypto

import (
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
)

// SignDigest 直接對 32 字節摘要做 RFC6979 確定性 ECDSA，返回 DER 編碼。
// 用於跨鏈消息等「摘要已由上層確定」的場景，不再內部重複哈希。
func SignDigest(digest, privateKey []byte) ([]byte, error) {
	if len(digest) != 32 {
		return nil, fmt.Errorf("crypto: 摘要必須為 32 字節，當前 %d", len(digest))
	}
	priv, _ := btcec.PrivKeyFromBytes(privateKey)
	return ecdsa.Sign(priv, digest).Serialize(), nil
}

// VerifyDigest 用壓縮/未壓縮公鑰直接驗證摘要的 DER 簽名。
func VerifyDigest(pubkey, digest, signature []byte) bool {
	if len(digest) != 32 {
		return false
	}
	pub, err := btcec.ParsePubKey(pubkey)
	if err != nil {
		return false
	}
	sig, err := ecdsa.ParseDERSignature(signature)
	if err != nil {
		return false
	}
	return sig.Verify(digest, pub)
}

// SignDigestKP 以密鑰對直接對摘要簽名（DER）。
func (k *KeyPair) SignDigestKP(digest []byte) ([]byte, error) {
	if len(digest) != 32 {
		return nil, fmt.Errorf("crypto: 摘要必須為 32 字節")
	}
	return ecdsa.Sign(k.priv, digest).Serialize(), nil
}
