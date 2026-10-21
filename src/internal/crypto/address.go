package crypto

import (
	"errors"
	"fmt"
	"strings"

	"github.com/btcsuite/btcd/btcec/v2"
)

// DefaultAddrHRP 為 TAC 地址的默認人類可讀前綴。
const DefaultAddrHRP = "tx0"

// addrHRP 為當前生效的地址前綴，可由 SetAddrHRP 覆蓋（通常由 config 同步）。
var addrHRP = DefaultAddrHRP

// SetAddrHRP 設置地址前綴（自動小寫、去空白）。
func SetAddrHRP(hrp string) {
	addrHRP = strings.ToLower(strings.TrimSpace(hrp))
}

// AddrHRP 返回當前地址前綴。
func AddrHRP() string { return addrHRP }

// compressPubKey 將 64 字節未壓縮公鑰（X||Y）轉為 33 字節壓縮格式；33 字節輸入原樣返回。
func compressPubKey(pubkey []byte) ([]byte, error) {
	switch len(pubkey) {
	case 33:
		return pubkey, nil
	case 64:
		uncompressed := make([]byte, 0, 65)
		uncompressed = append(uncompressed, 0x04)
		uncompressed = append(uncompressed, pubkey...)
		pub, err := btcec.ParsePubKey(uncompressed)
		if err != nil {
			return nil, fmt.Errorf("crypto: 解析公鑰失敗: %w", err)
		}
		return pub.SerializeCompressed(), nil
	default:
		return nil, fmt.Errorf("crypto: 非法公鑰長度=%d", len(pubkey))
	}
}

// PubKeyToAddress 從公鑰派生 tx0 地址。支持 33 字節壓縮 / 64 字節未壓縮（X||Y）輸入，
// 流程：規範為壓縮格式 → SHA-256 → RIPEMD-160 → 版本字節 → Base58Check → 加前綴。
func PubKeyToAddress(pubkey []byte) (string, error) {
	canon := pubkey
	if len(pubkey) == 64 {
		if c, err := compressPubKey(pubkey); err == nil {
			canon = c
		}
		// 與 Python 一致：無效公鑰不報錯，按原字節繼續哈希。
	}
	h160 := Hash160(canon)
	payload := make([]byte, 0, addressPayloadLen)
	payload = append(payload, AddrVersionByte)
	payload = append(payload, h160...)
	return addrHRP + base58CheckEncode(payload), nil
}

// Hash160ToAddress 把 20 字節 hash160 編碼為 tx0 地址。
func Hash160ToAddress(hash160 []byte) string {
	payload := append([]byte{AddrVersionByte}, hash160...)
	return addrHRP + base58CheckEncode(payload)
}

// IsValidAddress 校驗 tx0 地址：前綴大小寫不敏感，Base58 部分大小寫敏感並驗證版本字節與校驗和。
func IsValidAddress(address string) bool {
	if address == "" {
		return false
	}
	address = strings.TrimSpace(address)
	if !strings.HasPrefix(strings.ToLower(address), addrHRP) {
		return false
	}
	b58Part := address[len(addrHRP):]
	payload, err := base58CheckDecode(b58Part)
	if err != nil {
		return false
	}
	if len(payload) != addressPayloadLen {
		return false
	}
	return payload[0] == AddrVersionByte
}

// AddressToHash160 從合法地址提取 20 字節公鑰哈希。
func AddressToHash160(address string) ([]byte, error) {
	if !IsValidAddress(address) {
		return nil, fmt.Errorf("crypto: 非法地址: %s", address)
	}
	b58Part := address[len(addrHRP):]
	payload, err := base58CheckDecode(b58Part)
	if err != nil {
		return nil, err
	}
	return payload[1:], nil
}

// PrivateKeyToWIF 將 32 字節私鑰編碼為 WIF（含壓縮標記）。
func PrivateKeyToWIF(privateKey []byte) (string, error) {
	if len(privateKey) != 32 {
		return "", fmt.Errorf("%w，當前=%d", ErrPrivateKeyLength, len(privateKey))
	}
	payload := make([]byte, 0, 34)
	payload = append(payload, WIFVersionByte)
	payload = append(payload, privateKey...)
	payload = append(payload, CompressedFlag)
	return base58CheckEncode(payload), nil
}

// ErrWIF 表示 WIF 格式非法。
var ErrWIF = errors.New("crypto: 非法 WIF")

// WIFToPrivateKey 從 WIF 還原 32 字節私鑰，兼容壓縮（34）與未壓縮（33）。
func WIFToPrivateKey(wif string) ([]byte, error) {
	payload, err := base58CheckDecode(wif)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrWIF, err)
	}
	if len(payload) < 1 || payload[0] != WIFVersionByte {
		return nil, fmt.Errorf("%w: 版本字節錯誤", ErrWIF)
	}
	switch {
	case len(payload) == 34 && payload[33] == CompressedFlag:
		return payload[1:33], nil
	case len(payload) == 33:
		return payload[1:33], nil
	default:
		return nil, fmt.Errorf("%w: payload 長度=%d", ErrWIF, len(payload))
	}
}

// KeyPairFromWIF 從 WIF 恢復完整密鑰對。
func KeyPairFromWIF(wif string) (*KeyPair, error) {
	privateKey, err := WIFToPrivateKey(wif)
	if err != nil {
		return nil, err
	}
	return KeyPairFromPrivateKey(privateKey)
}
