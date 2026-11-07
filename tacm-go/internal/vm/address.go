package vm

import (
	"encoding/hex"
	"math/big"
	"strings"
)

// ZeroAddress 為全零地址。
var ZeroAddress = "0x" + strings.Repeat("0", 40)

// NormalizeAddress 統一地址為 0x+40hex 小寫；接受 string（0x 可選）、
// *big.Int 或 []byte（不足補零、過長取末 20 字節）。
func NormalizeAddress(addr any) string {
	switch v := addr.(type) {
	case *big.Int:
		return "0x" + hex.EncodeToString(IntToBytes(v, AddrByteLen))
	case []byte:
		b := v
		if len(b) < AddrByteLen {
			padded := make([]byte, AddrByteLen)
			copy(padded[AddrByteLen-len(b):], b)
			b = padded
		} else if len(b) > AddrByteLen {
			b = b[len(b)-AddrByteLen:]
		}
		return "0x" + hex.EncodeToString(b)
	case string:
		s := strings.TrimSpace(strings.ToLower(v))
		s = strings.TrimPrefix(s, "0x")
		if len(s) < 40 {
			s = strings.Repeat("0", 40-len(s)) + s
		} else if len(s) > 40 {
			s = s[len(s)-40:]
		}
		return "0x" + s
	default:
		return ZeroAddress
	}
}
