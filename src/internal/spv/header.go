package spv

import (
	"encoding/json"
	"fmt"

	"tacm/internal/crypto"
)

func hdrInt(m map[string]any, key string) int64 {
	switch v := m[key].(type) {
	case json.Number:
		n, _ := v.Int64()
		return n
	case float64:
		return int64(v)
	}
	return 0
}

func hdrStr(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	}
	return ""
}

// VerifyHeaderPoW 驗證區塊頭工作量證明。
func VerifyHeaderPoW(h map[string]any) bool {
	diff := int(hdrInt(h, "difficulty"))
	if diff <= 0 {
		diff = 1
	}
	ok, err := crypto.VerifyPow(h, diff)
	return err == nil && ok
}

// VerifyHeaderChain 驗證一組連續區塊頭：高度連續、prev_hash 鏈接、每塊 PoW 有效。
func VerifyHeaderChain(headers []map[string]any) (bool, string) {
	if len(headers) == 0 {
		return false, "empty header chain"
	}
	var prev map[string]any
	for _, h := range headers {
		if prev != nil {
			height := hdrInt(h, "height")
			prevHeight := hdrInt(prev, "height")
			if height != prevHeight+1 {
				return false, fmt.Sprintf("height gap @ %d", height)
			}
			if hdrStr(h, "prev_hash") != hdrStr(prev, "hash") {
				return false, fmt.Sprintf("prev_hash broken @ %d", height)
			}
			if !VerifyHeaderPoW(h) {
				return false, fmt.Sprintf("invalid PoW @ %d", height)
			}
		}
		prev = h
	}
	return true, "ok"
}
