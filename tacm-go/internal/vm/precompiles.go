package vm

// M134-A：EVM Precompiles（0x01-0x09）——ecrecover/sha256/ripemd160/identity/VRF。
// 主流鏈（ETH/BSC）合約常依賴 precompile（簽名驗證、ERC-1271、hash 計算）；
// 先前此類地址被當 EOA 處理，本模組補齊並與以太 gas 計費一致。

import (
	"crypto/sha256"
	"encoding/binary"
	"math/big"

	"golang.org/x/crypto/ripemd160"

	"tacm/internal/crypto"
)

// precompileGas 依以太規範計費（EIP-2028 前傳統費率）。
func precompileGas(addr uint64, inLen int) uint64 {
	words := (inLen + 31) / 32
	switch addr {
	case 1: // ecrecover
		return 3000
	case 2: // sha256：60 + 12/word
		return 60 + uint64(words)*12
	case 3: // ripemd160：600 + 120/word
		return 600 + uint64(words)*120
	case 4: // identity：15 + 3/word
		return 15 + uint64(words)*3
	case 9: // VRF（RANDAO 式）：固定費用
		return 30000
	}
	return 0
}

// runPrecompile 執行 precompile；matched=true 表示地址屬 precompile 範圍。
// 以太規範：ecrecover 失敗回空 bytes（非 revert）；gas 一律照扣。
func runPrecompile(ctx *ExecutionContext, to *big.Int, input []byte) (out []byte, matched bool, gasUsed uint64, err error) {
	if to == nil {
		return nil, false, 0, nil
	}
	addr := to.Uint64()
	if addr < 1 || addr > 9 {
		return nil, false, 0, nil
	}
	gasUsed = precompileGas(addr, len(input))
	switch addr {
	case 1: // ecrecover：input = hash(32) || v(32) || r(32) || s(32)
		if len(input) < 128 {
			return []byte{}, true, gasUsed, nil // 輸入不足 → 空（以太語意）
		}
		rec, rerr := crypto.RecoverEthPrecompile(input[0:32], input[32:64], input[64:96], input[96:128])
		if rerr != nil {
			return []byte{}, true, gasUsed, nil
		}
		return rec, true, gasUsed, nil
	case 2: // sha256
		h := sha256.Sum256(input)
		return h[:], true, gasUsed, nil
	case 3: // ripemd160 → 32-byte left-padded
		h := ripemd160.New()
		_, _ = h.Write(input)
		digest := h.Sum(nil)
		out := make([]byte, 32)
		copy(out[12:], digest)
		return out, true, gasUsed, nil
	case 4: // identity
		return append([]byte(nil), input...), true, gasUsed, nil
	case 9: // M151c：VRF 鏈上隨機數（RANDAO 式、全節點確定性）。
		// out = keccak256(keccak256(input) || uint64be(block_number) || uint64be(timestamp) || coinbase)。
		// 同區塊同輸入 → 同輸出（可重放）；跨區塊（timestamp 每塊不同）→ 不同（不可事前預測下一區塊）。
		var seed [32]byte
		if len(input) > 0 {
			h := crypto.Keccak256(input)
			copy(seed[:], h)
		}
		var nb [8]byte
		var ts [8]byte
		var cb []byte
		if ctx != nil {
			binary.BigEndian.PutUint64(nb[:], uint64(ctx.BlockNumber))
			binary.BigEndian.PutUint64(ts[:], uint64(ctx.BlockTimestamp))
			cb = []byte(ctx.BlockCoinbase)
		}
		return crypto.Keccak256(seed[:], nb[:], ts[:], cb), true, gasUsed, nil
	}
	return nil, false, 0, nil
}
