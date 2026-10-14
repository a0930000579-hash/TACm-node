package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
)

// ErrPrivateKeyLength 表示私鑰長度非法。
var ErrPrivateKeyLength = errors.New("crypto: 私鑰必須為 32 字節")

// KeyPair 封裝一對 ECDSA secp256k1 密鑰。
// secp256k1 為公開橢圓曲線標準，並不使 TAC 鏈依附於任何外部公鏈。
type KeyPair struct {
	priv *btcec.PrivateKey
}

// GenerateKeyPair 生成新的 secp256k1 密鑰對。
func GenerateKeyPair() (*KeyPair, error) {
	priv, err := btcec.NewPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("crypto: 生成密鑰對失敗: %w", err)
	}
	return &KeyPair{priv: priv}, nil
}

// DeriveKey 以 HMAC-SHA256(seed, label) 確定性派生子密鑰對。
// 用於 DEX 等需要「無私鑰保管的確定性帳戶」場景（池帳戶）；同一
// (seed,label) 恆得到同一地址，且不暴露主私鑰。
func DeriveKey(seed []byte, label string) (*KeyPair, error) {
	if len(seed) == 0 {
		return nil, errors.New("crypto: 派生種子為空")
	}
	mac := hmac.New(sha256.New, seed)
	if _, err := mac.Write([]byte(label)); err != nil {
		return nil, fmt.Errorf("crypto: 派生失敗: %w", err)
	}
	digest := mac.Sum(nil)
	// 私鑰需落在 [1, N-1]；取 mod N 保證有效（N 為 secp256k1 階）。
	n := btcec.S256().N
	k := new(big.Int).SetBytes(digest)
	k.Mod(k, new(big.Int).Sub(n, big.NewInt(1)))
	k.Add(k, big.NewInt(1))
	return KeyPairFromPrivateKey(k.FillBytes(make([]byte, 32)))
}

// KeyPairFromPrivateKey 從 32 字節私鑰恢復密鑰對。
func KeyPairFromPrivateKey(privateKey []byte) (*KeyPair, error) {
	if len(privateKey) != 32 {
		return nil, fmt.Errorf("%w，當前=%d", ErrPrivateKeyLength, len(privateKey))
	}
	priv, _ := btcec.PrivKeyFromBytes(privateKey)
	return &KeyPair{priv: priv}, nil
}

// PrivateKey 返回 32 字節私鑰。
func (k *KeyPair) PrivateKey() []byte {
	return k.priv.Serialize()
}

// PublicKey 返回 64 字節未壓縮公鑰（X||Y）。
func (k *KeyPair) PublicKey() []byte {
	raw := k.priv.PubKey().SerializeUncompressed() // 0x04 || X || Y
	return raw[1:]
}

// PublicKeyCompressed 返回 33 字節壓縮公鑰（02/03 || X）。
func (k *KeyPair) PublicKeyCompressed() []byte {
	return k.priv.PubKey().SerializeCompressed()
}

// Address 從壓縮公鑰派生 tx0 開頭的 Base58Check 地址。
func (k *KeyPair) Address() (string, error) {
	return PubKeyToAddress(k.PublicKeyCompressed())
}

// EthAddress 回以太格式地址（0x + keccak256(uncompressed[1:]) 尾 20 bytes）。
func (k *KeyPair) EthAddress() (string, error) {
	uncomp := k.PublicKey()
	digest := Keccak256(uncomp)
	if len(digest) < 20 {
		return "", errors.New("crypto: keccak 長度異常")
	}
	return EthAddressHex(digest[len(digest)-20:]), nil
}

// WIF 返回私鑰的 WIF 格式字符串（壓縮標記）。
func (k *KeyPair) WIF() (string, error) {
	return PrivateKeyToWIF(k.PrivateKey())
}

// Sign 對消息做 ECDSA 簽名：先 SHA-256(message) 得到摘要，再以 RFC6979 確定性方式簽名，返回 DER 編碼。
// 與 Python KeyPair.sign_deterministic(message, hashfunc=sha256) 等價。
func (k *KeyPair) Sign(message []byte) ([]byte, error) {
	digest := SHA256(message)
	sig := ecdsa.Sign(k.priv, digest)
	if sig == nil {
		return nil, errors.New("crypto: 簽名失敗")
	}
	return sig.Serialize(), nil
}

// VerifyWithPublicKey 用壓縮（33）或未壓縮（64）公鑰驗證消息的 DER 簽名。
func VerifyWithPublicKey(pubkey, message, signature []byte) bool {
	pub, err := btcec.ParsePubKey(pubkey)
	if err != nil {
		return false
	}
	sig, err := ecdsa.ParseDERSignature(signature)
	if err != nil {
		return false
	}
	return sig.Verify(SHA256(message), pub)
}

// Verify 驗證 DER 簽名是否對應本密鑰對與消息。任何解析/驗證失敗均返回 false。
func (k *KeyPair) Verify(message, signature []byte) bool {
	digest := SHA256(message)
	sig, err := ecdsa.ParseDERSignature(signature)
	if err != nil {
		return false
	}
	return sig.Verify(digest, k.priv.PubKey())
}

// KeyPairExport 用於安全地導出展示信息（私鑰需調用方自行妥善保存）。
type KeyPairExport struct {
	PrivateKeyHex string `json:"private_key_hex"`
	PublicKeyHex  string `json:"public_key_hex"`
	Address       string `json:"address"`
	WIF           string `json:"wif"`
}

// Export 導出密鑰對的展示信息。
func (k *KeyPair) Export() (*KeyPairExport, error) {
	addr, err := k.Address()
	if err != nil {
		return nil, err
	}
	wif, err := k.WIF()
	if err != nil {
		return nil, err
	}
	return &KeyPairExport{
		PrivateKeyHex: fmt.Sprintf("%x", k.PrivateKey()),
		PublicKeyHex:  fmt.Sprintf("%x", k.PublicKeyCompressed()),
		Address:       addr,
		WIF:           wif,
	}, nil
}
