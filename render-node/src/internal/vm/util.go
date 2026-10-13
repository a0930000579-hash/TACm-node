// Package vm 實現 TAC Ai 智能鏈的執行層（TAC VM，EVM 兼容）：
// 世界狀態（賬戶模型）、字節碼解釋、gas 計量、合約部署與消息調用。
package vm

import (
	"math/big"

	"golang.org/x/crypto/sha3"
)

const (
	MaxStackSize = 1024
	MaxCallDepth = 1024
	MaxCodeSize  = 0x6000 // 24576
	AddrByteLen  = 20
	WordByteLen  = 32
	MaxExecSteps = 1_000_000
)

var (
	big1    = big.NewInt(1)
	big0    = big.NewInt(0)
	tt255   = new(big.Int).Lsh(big.NewInt(1), 255) // 2^255
	tt256   = new(big.Int).Lsh(big.NewInt(1), 256) // 2^256
	tt256m1 = new(big.Int).Sub(tt256, big.NewInt(1))

	// MaxUint256 = 2^256 - 1。
	MaxUint256 = new(big.Int).Set(tt256m1)
)

// U256 把整數截斷為 256 位無符號（返回新值，不改輸入）。
func U256(x *big.Int) *big.Int { return new(big.Int).And(x, MaxUint256) }

// ToSigned 把 256 位無符號解釋為有符號整數。
func ToSigned(x *big.Int) *big.Int {
	v := U256(x)
	if v.Cmp(tt255) >= 0 {
		return new(big.Int).Sub(v, tt256)
	}
	return v
}

// FromSigned 把有符號整數編碼為 256 位無符號。
func FromSigned(x *big.Int) *big.Int {
	if x.Sign() < 0 {
		return new(big.Int).Add(x, tt256)
	}
	return new(big.Int).Set(x)
}

// BytesToInt 把大字端字節解為整數（空=0）。
func BytesToInt(b []byte) *big.Int { return new(big.Int).SetBytes(b) }

// IntToBytes 把整數編為固定長度大字端字節（截斷/左補零）。
func IntToBytes(x *big.Int, length int) []byte {
	b := U256(x).Bytes()
	if len(b) >= length {
		return b[len(b)-length:]
	}
	out := make([]byte, length)
	copy(out[length-len(b):], b)
	return out
}

// Keccak256 返回 EVM 使用的 Keccak-256（非 NIST SHA3-256）摘要。
func Keccak256(parts ...[]byte) []byte {
	h := sha3.NewLegacyKeccak256()
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

// Keccak256Hash 以 *big.Int 返回 Keccak-256 摘要。
func Keccak256Hash(parts ...[]byte) *big.Int { return BytesToInt(Keccak256(parts...)) }
