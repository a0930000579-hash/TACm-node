package crypto

import (
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
)

// Ethereum 錢包簽名標準：
//   - HashPersonalMessage（EIP-191）：keccak256("\x19Ethereum Signed Message:\n" ‖ len ‖ msg)
//   - SignEthereumMessage：對訊息哈希回 65-byte compact 簽名（v=27+recid）
//   - RecoverSignerFromSig：從 65-byte 簽名恢復簽名者地址
// 對應 RPC：personal_sign / eth_sign / eth_ecRecover / personal_ecRecover。

// HashPersonalMessage 計算 EIP-191 個人訊息哈希。
func HashPersonalMessage(msg []byte) []byte {
	lenStr := fmt.Sprintf("%d", len(msg))
	return Keccak256([]byte("\x19Ethereum Signed Message:\n" + lenStr), msg)
}

// SignEthereumMessage 對 32-byte 哈希簽名，回 65-byte compact（v=27+recid）。
func SignEthereumMessage(hash, privateKey []byte) ([]byte, error) {
	if len(hash) != 32 {
		return nil, fmt.Errorf("crypto: 簽名哈希需 32 字節，當前 %d", len(hash))
	}
	priv, _ := btcec.PrivKeyFromBytes(privateKey)
	sig, err := ecdsa.SignCompact(priv, hash, false)
	if err != nil {
		return nil, fmt.Errorf("crypto: ECDSA 簽名失敗: %w", err)
	}
	if len(sig) != 65 {
		return nil, fmt.Errorf("crypto: compact 簽名長度異常 %d", len(sig))
	}
	// 以太慣例 recid ∈ {0,1}（v=27/28）。btcec 偶爾回 recid 2/3
	// （x ≥ curve N）：依 BIP-137 調整，r 減去 N 後 recid 減 2。
	recid := int(sig[0]&0x03) - 27 + 27
	recid = int(sig[0]&0x03) - 0
	recid = int(sig[0] - 27)
	if recid >= 2 {
		r := new(big.Int).SetBytes(sig[1:33])
		r.Sub(r, btcec.S256().N)
		if r.Sign() < 0 {
			return nil, errors.New("crypto: 簽名 r 調整為負")
		}
		r.FillBytes(sig[1:33])
		sig[0] -= 2
	}
	return sig, nil
}

// SignPersonalMessage 對任意訊息做 EIP-191 簽名（personal_sign 全流程）。
func SignPersonalMessage(msg, privateKey []byte) ([]byte, error) {
	return SignEthereumMessage(HashPersonalMessage(msg), privateKey)
}

// RecoverSignerFromSig 從 65-byte compact 簽名（v=27/28）恢復簽名者公鑰。
func RecoverSignerFromSig(hash, sig []byte) (*btcec.PublicKey, error) {
	if len(hash) != 32 {
		return nil, fmt.Errorf("crypto: 哈希需 32 字節，當前 %d", len(hash))
	}
	if len(sig) != 65 {
		return nil, fmt.Errorf("crypto: 簽名需 65 字節，當前 %d", len(sig))
	}
	if sig[0] < 27 || sig[0] > 30 {
		return nil, fmt.Errorf("crypto: 簽名 v 無效 %d", sig[0])
	}
	pub, _, err := ecdsa.RecoverCompact(sig, hash)
	if err != nil {
		return nil, fmt.Errorf("crypto: 簽名恢復失敗: %w", err)
	}
	return pub, nil
}

// RecoverSignerAddressFromSig 從 65-byte 簽名恢復簽名者以太地址（0x…40）。
// 以太慣例：keccak256(uncompressedPubkey[1:]) 尾 20 bytes。
func RecoverSignerAddressFromSig(hash, sig []byte) (string, error) {
	pub, err := RecoverSignerFromSig(hash, sig)
	if err != nil {
		return "", err
	}
	uncomp := pub.SerializeUncompressed()
	digest := Keccak256(uncomp[1:])
	if len(digest) < 20 {
		return "", errors.New("crypto: keccak 長度異常")
	}
	return EthAddressHex(digest[len(digest)-20:]), nil
}

// ParseEthereumSig 解析 0x…65 bytes 簽名為 r/s/v（big.Int 形式，v=27/28）。
func ParseEthereumSig(sigHex string) (r, s *big.Int, v byte, err error) {
	h := strings.TrimPrefix(strings.TrimPrefix(sigHex, "0x"), "0X")
	if len(h) != 130 {
		return nil, nil, 0, errors.New("crypto: 簽名 hex 需 130 字元（65 bytes）")
	}
	b, derr := hexDecode(h)
	if derr != nil {
		return nil, nil, 0, derr
	}
	r = new(big.Int).SetBytes(b[1:33])
	s = new(big.Int).SetBytes(b[33:65])
	v = b[0]
	return r, s, v, nil
}
