package crypto

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// NormalizeBlockHeader 按白名單規範化區塊頭（字段類型固定、merkle_root 轉小寫），
// 與 Python _normalize_block_header 等價。
func NormalizeBlockHeader(header map[string]any) map[string]any {
	return map[string]any{
		"height":      asInt(header["height"]),
		"prev_hash":   header["prev_hash"], // 可能為 nil，交由 canonical 輸出 null
		"merkle_root": strings.ToLower(asString(header["merkle_root"])),
		"proposer":    asString(header["proposer"]),
		"ts":          asInt(header["ts"]),
		"tx_count":    asInt(header["tx_count"]),
		"difficulty":  asInt(header["difficulty"]),
		"nonce":       asInt(header["nonce"]),
	}
}

// HashBlockHeader 對規範化區塊頭做雙重 SHA-256，返回 hex。
func HashBlockHeader(header map[string]any) (string, error) {
	raw, err := Canonical(NormalizeBlockHeader(header))
	if err != nil {
		return "", fmt.Errorf("crypto: 區塊頭序列化失敗: %w", err)
	}
	return fmt.Sprintf("%x", DoubleSHA256(raw)), nil
}

// asInt 寬鬆地把常見 JSON/標量類型轉為 int。
func asInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case uint64:
		return int(t)
	case float64:
		return int(t)
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return int(n)
		}
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
			return n
		}
	case nil:
		return 0
	}
	return 0
}

// asString 寬鬆地把標量轉為字符串（nil → ""）。
func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}
