package addrs

import (
	"encoding/binary"
	"strconv"
)

// BLAKE2s 常量與算法（RFC 7693），自包含實現以支持任意 digest_size（1..32）。
// 與 Go 官方 x/crypto/blake2s 的參數塊初始化規則一致，從而與 Python hashlib.blake2s 對齊。

var b2sIV = [8]uint32{
	0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a,
	0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19,
}

// b2sSigma 為 10 輪消息調度表。
var b2sSigma = [10][16]uint8{
	{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
	{14, 10, 4, 8, 9, 15, 13, 6, 1, 12, 0, 2, 11, 7, 5, 3},
	{11, 8, 12, 0, 5, 2, 15, 13, 10, 14, 3, 6, 7, 1, 9, 4},
	{7, 9, 3, 1, 13, 12, 11, 14, 2, 6, 5, 10, 4, 0, 15, 8},
	{9, 0, 5, 7, 2, 4, 10, 15, 14, 1, 11, 12, 6, 8, 3, 13},
	{2, 12, 6, 10, 0, 11, 8, 3, 4, 13, 7, 5, 15, 14, 1, 9},
	{12, 5, 1, 15, 14, 13, 4, 10, 0, 7, 6, 3, 9, 2, 8, 11},
	{13, 11, 7, 14, 12, 1, 3, 9, 5, 0, 15, 4, 8, 6, 2, 10},
	{6, 15, 14, 9, 11, 3, 0, 8, 12, 2, 13, 7, 1, 4, 10, 5},
	{10, 2, 8, 4, 7, 6, 1, 5, 15, 11, 9, 14, 3, 12, 13, 0},
}

func rotr32(x uint32, n uint) uint32 { return (x >> n) | (x << (32 - n)) }

// b2sG 為 BLAKE2s 混合函數。
func b2sG(v *[16]uint32, a, b, c, d int, x, y uint32) {
	v[a] = v[a] + v[b] + x
	v[d] = rotr32(v[d]^v[a], 16)
	v[c] = v[c] + v[d]
	v[b] = rotr32(v[b]^v[c], 12)
	v[a] = v[a] + v[b] + y
	v[d] = rotr32(v[d]^v[a], 8)
	v[c] = v[c] + v[d]
	v[b] = rotr32(v[b]^v[c], 7)
}

// b2sCompress 對一個 64 字節塊做 BLAKE2s 壓縮，就地更新 h。
func b2sCompress(h *[8]uint32, block *[64]byte, t0, t1 uint32, last bool) {
	var m [16]uint32
	for i := 0; i < 16; i++ {
		m[i] = binary.LittleEndian.Uint32(block[4*i:])
	}
	var v [16]uint32
	copy(v[:8], h[:])
	copy(v[8:], b2sIV[:])
	v[12] ^= t0
	v[13] ^= t1
	if last {
		v[14] ^= 0xffffffff
	}
	for r := 0; r < 10; r++ {
		s := &b2sSigma[r]
		b2sG(&v, 0, 4, 8, 12, m[s[0]], m[s[1]])
		b2sG(&v, 1, 5, 9, 13, m[s[2]], m[s[3]])
		b2sG(&v, 2, 6, 10, 14, m[s[4]], m[s[5]])
		b2sG(&v, 3, 7, 11, 15, m[s[6]], m[s[7]])
		b2sG(&v, 0, 5, 10, 15, m[s[8]], m[s[9]])
		b2sG(&v, 1, 6, 11, 12, m[s[10]], m[s[11]])
		b2sG(&v, 2, 7, 8, 13, m[s[12]], m[s[13]])
		b2sG(&v, 3, 4, 9, 14, m[s[14]], m[s[15]])
	}
	for i := 0; i < 8; i++ {
		h[i] ^= v[i] ^ v[i+8]
	}
}

// SumBlake2s 返回 data 的參數化 BLAKE2s 摘要（取前 size 字節，size 須在 1..32）。
// 無密鑰、順序模式（fanout=1, depth=1），與 Python hashlib.blake2s(data, digest_size=size) 等價。
func SumBlake2s(data []byte, size int) ([]byte, error) {
	if size < 1 || size > 32 {
		return nil, &Blake2sSizeError{Size: size}
	}
	h := b2sIV
	// 參數塊前 4 字節：digest_length=size, key_length=0, fanout=1, depth=1。
	h[0] ^= uint32(size) | (1 << 16) | (1 << 24)

	total := len(data)
	rest := data
	// 處理除「最後一塊」外的完整 64 字節塊（長度恰為 64 倍數時最後一塊留作 final）。
	for len(rest) > 64 {
		var block [64]byte
		copy(block[:], rest[:64])
		b2sCompress(&h, &block, uint32(total-len(rest)+64), 0, false)
		rest = rest[64:]
	}
	// 最後一塊（不足 64 字節補零），計數器為總字節數，置 last 標誌。
	var block [64]byte
	copy(block[:], rest)
	b2sCompress(&h, &block, uint32(total), 0, true)

	full := make([]byte, 32)
	for i := 0; i < 8; i++ {
		binary.LittleEndian.PutUint32(full[4*i:], h[i])
	}
	return full[:size], nil
}

// Blake2sSizeError 表示請求的摘要長度非法。
type Blake2sSizeError struct{ Size int }

func (e *Blake2sSizeError) Error() string {
	return "addrs: blake2s 摘要長度須在 1..32，當前=" + strconv.Itoa(e.Size)
}
