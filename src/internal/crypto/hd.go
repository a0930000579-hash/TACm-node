package crypto

// v58：標準 BIP-32/39/44 HD 錢包（secp256k1）。
// 主流鏈錢包兼容：MetaMask/Truffle/Hardhat 以 m/44'/60'/0'/0/i 派生 ETH 帳戶；
// 助記詞→種子（PBKDF2 2048 輪）→BIP-32 主節點→CKD 子密鑰派生。

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/btcsuite/btcd/btcec/v2"
	"golang.org/x/crypto/pbkdf2"
)

// HDNode 為一個 BIP-32 節點（私鑰＋鏈碼）。
type HDNode struct {
	key       []byte // 32B 私鑰
	chainCode []byte // 32B
	depth     byte
	index     uint32
}

// HDPathBIP44 為 BIP-44 以太坊帳戶路徑（MetaMask 兼容）。
const HDPathBIP44 = "m/44'/60'"

// ErrHDInvalidChild 表示 BIP-32 子密鑰派生失敗（IL≥n 或子私鑰=0，規範要求換 index）。
var ErrHDInvalidChild = errors.New("crypto: BIP-32 子密鑰無效（IL ≥ n 或為 0）")

// MnemonicToSeed 以 BIP-39 標準將助記詞＋口令轉為 64 字節種子（PBKDF2-HMAC-SHA512，2048 輪）。
func MnemonicToSeed(mnemonic, passphrase string) ([]byte, error) {
	m := strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(mnemonic))), " ")
	if m == "" {
		return nil, errors.New("crypto: 助記詞為空")
	}
	salt := "mnemonic" + passphrase
	return pbkdf2.Key([]byte(m), []byte(salt), 2048, 64, sha512.New), nil
}

// MasterKeyFromSeed 從種子產生 BIP-32 主節點（I = HMAC-SHA512("Bitcoin seed", seed)）。
func MasterKeyFromSeed(seed []byte) (*HDNode, error) {
	if len(seed) < 16 || len(seed) > 64 {
		return nil, fmt.Errorf("crypto: 種子長度 %d 不在 [16,64]", len(seed))
	}
	mac := hmac.New(sha512.New, []byte("Bitcoin seed"))
	if _, err := mac.Write(seed); err != nil {
		return nil, fmt.Errorf("crypto: BIP-32 主節點 HMAC 失敗: %w", err)
	}
	I := mac.Sum(nil)
	il, ir := I[:32], I[32:]
	// IL 必須 < n 且非 0（私鑰有效）。
	if !validChildKey(il) {
		return nil, ErrHDInvalidChild
	}
	return &HDNode{key: clone(il), chainCode: clone(ir)}, nil
}

// Child 執行 BIP-32 子密鑰派生（CKD）：
//   normal（index < 2^31）：  data = SerP(pubkey) || Ser32(index)
//   hardened（index ≥ 2^31）：data = 0x00 || Ser256(priv) || Ser32(index)
//   I = HMAC-SHA512(chainCode, data)；childPriv = (IL + k) mod n；childChain = IR。
func (n *HDNode) Child(index uint32) (*HDNode, error) {
	if n == nil || len(n.key) != 32 || len(n.chainCode) != 32 {
		return nil, errors.New("crypto: BIP-32 節點未初始化")
	}
	var data []byte
	if index >= 1<<31 {
		// 硬化：0x00 || Ser256(k) || Ser32(index)
		data = make([]byte, 0, 37)
		data = append(data, 0x00)
		data = append(data, n.key...)
	} else {
		// 非硬化：SerP(壓縮公鑰) || Ser32(index)
		priv, _ := btcec.PrivKeyFromBytes(n.key)
		data = make([]byte, 0, 37)
		data = append(data, priv.PubKey().SerializeCompressed()...)
	}
	var idxBuf [4]byte
	binary.BigEndian.PutUint32(idxBuf[:], index)
	data = append(data, idxBuf[:]...)

	mac := hmac.New(sha512.New, n.chainCode)
	if _, err := mac.Write(data); err != nil {
		return nil, fmt.Errorf("crypto: BIP-32 CKD HMAC 失敗: %w", err)
	}
	I := mac.Sum(nil)
	il, ir := I[:32], I[32:]
	if !validChildKey(il) {
		return nil, ErrHDInvalidChild
	}
	// childPriv = (IL + k) mod n
	child := new(big.Int).Add(new(big.Int).SetBytes(il), new(big.Int).SetBytes(n.key))
	child.Mod(child, btcec.S256().N)
	if child.Sign() == 0 {
		return nil, ErrHDInvalidChild
	}
	return &HDNode{
		key:       child.FillBytes(make([]byte, 32)),
		chainCode: clone(ir),
		depth:     n.depth + 1,
		index:     index,
	}, nil
}

// ChildPath 沿路徑依序派生（支援硬化撇號，如 "m/44'/60'/0'/0/0"）。
func (n *HDNode) ChildPath(path string) (*HDNode, error) {
	indexes, err := ParseHDPath(path)
	if err != nil {
		return nil, err
	}
	cur := n
	for _, idx := range indexes {
		cur, err = cur.Child(idx)
		if err != nil {
			return nil, err
		}
	}
	return cur, nil
}

// PrivateKey 回本節點 32 字節私鑰。
func (n *HDNode) PrivateKey() []byte { return clone(n.key) }

// KeyPair 回本節點對應密鑰對。
func (n *HDNode) KeyPair() (*KeyPair, error) { return KeyPairFromPrivateKey(n.key) }

// ParseHDPath 解析 BIP-32 路徑字串（m/44'/60'/0'/0/0），
// 回傳逐級 index（硬化的 index 為 2^31 + 數字）。
func ParseHDPath(path string) ([]uint32, error) {
	p := strings.TrimSpace(path)
	if !strings.HasPrefix(p, "m") {
		return nil, fmt.Errorf("crypto: 路徑必須以 m 開頭: %q", path)
	}
	rest := strings.TrimPrefix(p, "m")
	rest = strings.TrimPrefix(rest, "/")
	if rest == "" {
		if strings.HasSuffix(p, "/") {
			return nil, fmt.Errorf("crypto: 路徑尾斜線無內容: %q", path)
		}
		return nil, nil
	}
	parts := strings.Split(rest, "/")
	out := make([]uint32, 0, len(parts))
	for _, part := range parts {
		hardened := false
		s := strings.TrimSpace(part)
		if strings.Count(s, "'")+strings.Count(s, "h")+strings.Count(s, "H") > 1 {
			return nil, fmt.Errorf("crypto: 路徑段硬化標記重複 %q", part)
		}
		if strings.HasSuffix(s, "'") || strings.HasSuffix(s, "h") || strings.HasSuffix(s, "H") {
			hardened = true
			s = strings.TrimRight(s, "'hH")
		}
		if s == "" {
			return nil, fmt.Errorf("crypto: 路徑段為空: %q", path)
		}
		v, ok := new(big.Int).SetString(s, 10)
		if !ok || v.Sign() < 0 || v.BitLen() > 31 {
			return nil, fmt.Errorf("crypto: 路徑段非法 %q", part)
		}
		idx := uint32(v.Uint64())
		if hardened {
			idx += 1 << 31
		}
		out = append(out, idx)
	}
	return out, nil
}

// DeriveBIP44Key 依 BIP-44 以太坊路徑 m/44'/60'/account'/0/index 派生帳戶私鑰。
func DeriveBIP44Key(seed []byte, account, index uint32) (*KeyPair, error) {
	master, err := MasterKeyFromSeed(seed)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("m/44'/60'/%d'/0/%d", account, index)
	node, err := master.ChildPath(path)
	if err != nil {
		return nil, err
	}
	return node.KeyPair()
}

// validChildKey 檢查 32 字節值可作為 secp256k1 私鑰（1 ≤ k < n）。
func validChildKey(k []byte) bool {
	if len(k) != 32 {
		return false
	}
	v := new(big.Int).SetBytes(k)
	if v.Sign() == 0 {
		return false
	}
	return v.Cmp(btcec.S256().N) < 0
}

func clone(b []byte) []byte {
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
