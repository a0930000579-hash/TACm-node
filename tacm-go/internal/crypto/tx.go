package crypto

import (
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
)

// txSignFields 為交易簽名的白名單字段，確保簽名內容確定、可跨節點復現。
var txSignFields = []string{"from", "to", "amount", "fee", "memo", "ts", "nonce", "token"}

// ErrTxNoPublicKey 表示交易未附帶公鑰，無法獨立驗證簽名。
var ErrTxNoPublicKey = errors.New("crypto: 交易缺少 pubkey 字段，無法驗證")

// TxSighash 對交易白名單字段做規範序列化後返回雙重 SHA-256 摘要。
func TxSighash(tx map[string]any) ([]byte, error) {
	filtered := map[string]any{}
	for _, k := range txSignFields {
		v, ok := tx[k]
		if !ok {
			// 可選文本字段缺失補空串，使簽名語境跨節點確定，
			// 不依賴該字段是否恰好存在於 map。
			if k == "memo" || k == "token" {
				v = ""
			} else {
				continue
			}
		}
		filtered[k] = v
	}
	raw, err := Canonical(filtered)
	if err != nil {
		return nil, fmt.Errorf("crypto: 交易序列化失敗: %w", err)
	}
	return DoubleSHA256(raw), nil
}

// SignTransaction 用私鑰簽名交易，返回 DER 編碼的十六進制簽名。
func SignTransaction(tx map[string]any, privateKey []byte) (string, error) {
	kp, err := KeyPairFromPrivateKey(privateKey)
	if err != nil {
		return "", err
	}
	sighash, err := TxSighash(tx)
	if err != nil {
		return "", err
	}
	sig, err := kp.Sign(sighash) // Sign 內部再做 SHA-256(sighash)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(sig), nil
}

// VerifyTransactionSignature 驗證交易簽名：需 tx 攜帶 pubkey（壓縮/未壓縮 hex），
// 先校驗公鑰派生地址與 signerAddress 一致，再校驗簽名。任何失敗返回 false。
func VerifyTransactionSignature(tx map[string]any, signatureHex, signerAddress string) bool {
	pubkeyHex, ok := tx["pubkey"].(string)
	if !ok || pubkeyHex == "" {
		return false
	}
	pub, err := parsePubKeyHex(pubkeyHex)
	if err != nil {
		return false
	}
	derived, err := PubKeyToAddress(pub.SerializeCompressed())
	if err != nil || derived != signerAddress {
		return false
	}
	sighash, err := TxSighash(tx)
	if err != nil {
		return false
	}
	return verifyWithPub(pub, sighash, signatureHex)
}

// VerifyTransactionWithPublicKey 使用顯式公鑰驗證簽名（不檢查地址匹配）。
func VerifyTransactionWithPublicKey(tx map[string]any, signatureHex, pubkeyHex string) bool {
	pub, err := parsePubKeyHex(pubkeyHex)
	if err != nil {
		return false
	}
	sighash, err := TxSighash(tx)
	if err != nil {
		return false
	}
	return verifyWithPub(pub, sighash, signatureHex)
}

// verifyWithPub 用給定公鑰驗證 DER 簽名；簽名哈希規則為 SHA-256(sighash)。
func verifyWithPub(pub *btcec.PublicKey, sighash []byte, signatureHex string) bool {
	sigBytes, err := hex.DecodeString(signatureHex)
	if err != nil {
		return false
	}
	sig, err := ecdsa.ParseDERSignature(sigBytes)
	if err != nil {
		return false
	}
	digest := SHA256(sighash)
	return sig.Verify(digest, pub)
}

// parsePubKeyHex 解析 33 字節壓縮或 64 字節未壓縮（X||Y）的公鑰 hex。
func parsePubKeyHex(pubkeyHex string) (*btcec.PublicKey, error) {
	b, err := hex.DecodeString(pubkeyHex)
	if err != nil {
		return nil, fmt.Errorf("crypto: 公鑰 hex 非法: %w", err)
	}
	switch len(b) {
	case 33:
		return btcec.ParsePubKey(b)
	case 64:
		uncompressed := make([]byte, 0, 65)
		uncompressed = append(uncompressed, 0x04)
		uncompressed = append(uncompressed, b...)
		return btcec.ParsePubKey(uncompressed)
	default:
		return nil, fmt.Errorf("crypto: 公鑰長度非法=%d", len(b))
	}
}
